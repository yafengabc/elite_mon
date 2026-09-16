// Elite Dangerous Journal Monitor
//
// Polls the ED journal incrementally every 2s, tracks bounties and shield drops,
// pushes alerts to WxPusher, and serves a live panel on :8088.
//
// This file and the web panel (static/) are shared by both build flavors:
//
//	go build             console build (a console window on Windows; also cross-compiles with GOOS=linux)
//	go build -tags gui   Windows GUI build (pure Win32 SDK window, no console)
//
// The only difference is the entry points: startLogging / runUI each have an
// implementation in gui.go and console.go, selected by build tag.
package main

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
)

//go:embed static/*
var staticFS embed.FS

const (
	configFileName = "config.toml"

	defaultListenAddr  = ":8088"
	defaultWxPusherURL = "https://wxpusher.zjiecode.com/api/send/message"

	// Max stall alerts to push. Once reached, pushing stops (and no more
	// duplicate lines are added to the panel), so a long stall does not emit
	// one alert per cfg.stall forever. Activity resets the counter, giving the
	// next stall another 3 alerts.
	stallPushMax = 3

	// Status.json Flags bits
	flagInFighter = 1 << 25
	flagInSRV     = 1 << 26

	// The bounty and message lists deliberately have no memory cap.
	//
	// Per-session stats are the whole point: both lists are cleared only when
	// the journal changes (i.e. a new session), so they are naturally bounded by
	// one game session. Measured over 431 real sessions: peak 879 kills /
	// 8490 message lines, ~0.14MB total — memory was never the issue.
	// A cap would silently undercount totals in very long sessions (aggregation
	// walks the whole list), contradicting "true totals for this session".
	// How many records the panel shows is controlled separately by cfg.MaxListLen.

	// Panel trend chart: one 10-minute bucket per cell, spanning the first to
	// the last kill of the current journal. The point cap is just a safety net
	// (unreachable in a normal login); when exceeded, the oldest points are
	// dropped to keep the recent stretch.
	//
	// The chart plots kills per hour, so the panel extrapolates a cell's own
	// count by 6 (TREND_CELLS_PER_HOUR in static/main.js): change the bucket and
	// that factor has to follow, or the two curves stop sharing a scale.
	trendBucket    = 10 * time.Minute
	maxTrendPoints = 720 // 720 × 10 min = 120 hours

	// Rolling window for the hour-average curve, matching the panel's
	// "last hour kills". Deliberately not tied to the configurable stats
	// window: changing that would change what the curve means, making
	// plotted history incomparable.
	trendRolling = time.Hour

	journalTimeLayout = "2006-01-02T15:04:05Z" // timestamp inside journal events
	sessionLayout     = "2006-01-02T150405"    // session time in the journal filename
	displayLayout     = "2006-01-02 15:04:05"  // time format used in the panel

	// Source label shown for MissionCompleted entries in the bounty log.
	// The real mission name is omitted: that localized text is far too long
	// for one line. The label falls back to T("bounty.mission") (see the
	// MissionCompleted handler) so it follows the UI language.
)

var (
	// cfgNotices are the config-load messages. loadConfig runs during package
	// init, before every init() has run - so the language tables are not
	// registered yet and it cannot translate anything. It buffers the messages
	// here instead and main() prints them once applyLang() has run.
	cfgNotices []pendingLog

	// cfg is loaded here (not in main) because startLogging() must be installed
	// first: the GUI build (-H windowsgui) has no stderr, so early log lines are
	// lost unless capture is up. In the console build startLogging is a no-op
	// (logs already go to stderr), so keeping it here is harmless.
	cfg = func() Config {
		startLogging()
		c, notices := loadConfig()
		cfgNotices = notices
		return c
	}()

	globalStatus AppStatus
	statusLock   sync.RWMutex
	// statusRev increments on every status publish so the GUI can tell whether
	// data changed and repaint on demand, instead of rebuilding the whole UI
	// tree every 500ms (pure churn while idle, wasting CPU and allocations).
	statusRev uint64
)

// ------------------------------------------------------------------
// Config
// ------------------------------------------------------------------

// Config mirrors config.toml. Keys are snake_case. Unknown keys are ignored and
// absent ones keep the built-in default, so a stale or partial file never
// blocks startup; the file itself is never rewritten (see loadConfig).
type Config struct {
	ListenAddr string `toml:"listen_addr"` // HTTP listen address

	// Display timezone. Journal timestamps are always UTC; this decides how the
	// panel and both UIs render time. Default UTC+8; also accepts
	// auto / UTC / UTC+5:30 (see tz.go).
	TimeZone string `toml:"timezone"`

	// Master switch for the web panel. false = never listen on a port; run
	// monitoring and WxPusher push only.
	EnablePanel bool `toml:"enable_panel"`

	// UI language. auto (the default) follows the system locale; 中文 / English
	// pin it. Takes effect after restart (see i18n.go and locale.go).
	Language string `toml:"language"`

	PollInterval     string `toml:"poll_interval"`      // e.g. "2s"
	StallThreshold   string `toml:"stall_threshold"`    // push after the journal stays silent this long, e.g. "10m"
	StatWindow       string `toml:"stat_window"`        // "last N" stats window, e.g. "1h"
	MaxListLen       int    `toml:"max_list_len"`       // max records returned per API call
	HistoryScanCount int    `toml:"history_scan_count"` // journals to re-scan when ship data is missing

	// WxPusher is a third-party push service (wxpusher.zjiecode.com), not an
	// official WeChat API. Alerts are delivered to WeChat through the service
	// account you bind on their side.
	WxPusher struct {
		URL      string `toml:"url"`
		AppToken string `toml:"app_token"`
		UID      string `toml:"uid"`
	} `toml:"wxpusher"`

	// Parsed durations; not serialized.
	poll  time.Duration
	stall time.Duration
	stat  time.Duration
}

// defaultConfig returns the built-in defaults. Private credentials (token/uid)
// are intentionally omitted here; each machine fills them in its own config.toml
// so secrets never land in source or travel to another computer.
func defaultConfig() Config {
	c := Config{
		ListenAddr:       defaultListenAddr,
		TimeZone:         "UTC+8",
		Language:         "auto",
		EnablePanel:      true,
		PollInterval:     "2s",
		StallThreshold:   "10m",
		StatWindow:       "1h",
		MaxListLen:       200,
		HistoryScanCount: 5,
	}

	c.WxPusher.URL = defaultWxPusherURL

	return c
}

// normalize validates the config and parses the duration strings.
func (c *Config) normalize() {
	c.poll = parseDuration(c.PollInterval, 2*time.Second)
	c.stall = parseDuration(c.StallThreshold, 10*time.Minute)
	c.stat = parseDuration(c.StatWindow, time.Hour)

	if c.ListenAddr == "" {
		c.ListenAddr = defaultListenAddr
	}
	if c.MaxListLen <= 0 {
		c.MaxListLen = 200
	}
	if c.HistoryScanCount <= 0 {
		c.HistoryScanCount = 5
	}
	if c.WxPusher.URL == "" {
		c.WxPusher.URL = defaultWxPusherURL
	}
}

// journalDir returns the default ED save directory (hardcoded, not configurable).
//
// One fallback covers both platforms: Windows only sets USERPROFILE, Linux only
// HOME, and the two-level layout is identical on both (Proton/Wine too), so a
// single function suffices.
func (c *Config) journalDir() string {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, "Saved Games", "Frontier Developments", "Elite Dangerous")
}

// wxEnabled reports whether WxPusher credentials are configured.
func (c *Config) wxEnabled() bool {
	return c.WxPusher.AppToken != "" && c.WxPusher.UID != ""
}

// pendingLog is a log line whose text depends on the language. loadConfig runs
// during package init, before the language tables are registered, so it records
// the line as a closure and main() evaluates it once applyLang() has run.
// Keeping the id inside a T() call also means i18n_check.py still sees it.
type pendingLog func() string

// loadConfig reads config.toml. Absent keys keep the built-in default (c starts
// from defaultConfig) and a file that does not parse falls back to defaults
// wholesale. It returns the config plus the messages to log; it never logs
// itself, because the language is not known this early (see pendingLog), and it
// never rewrites the file — a config the user hand-edits must not change
// underneath them. Migrating an older config.json is a one-off manual job, not
// something the binary does at every startup.
func loadConfig() (Config, []pendingLog) {
	c := defaultConfig()
	path := configPath()

	data, err := os.ReadFile(path)
	if err != nil {
		// First run: no config file, write a default one ready to edit.
		var notices []pendingLog
		if saveConfig(path, c) {
			notices = append(notices, func() string { return T("log.cfg_default_created", path) })
		} else {
			notices = append(notices, func() string { return T("log.cfg_write_failed", path) })
		}
		c.normalize()
		return c, notices
	}

	// A leading UTF-8 BOM needs no special handling: the TOML decoder skips one,
	// where encoding/json used to reject it and silently drop the whole config
	// (language and push credentials included).
	if _, err := toml.Decode(string(data), &c); err != nil {
		parseErr := err
		notices := []pendingLog{func() string { return T("log.cfg_parse_failed", parseErr) }}
		c = defaultConfig()
		c.normalize()
		return c, notices
	}

	c.normalize()
	return c, nil
}

// configPath locates the config file: next to the executable by default,
// overridable with ED_CONFIG.
func configPath() string {
	if p := strings.TrimSpace(os.Getenv("ED_CONFIG")); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), configFileName)
	}
	return configFileName
}

// configHeader documents the generated config.toml. TOML comments replace the
// "comment" array the JSON version needed, so the explanation can sit next to
// the keys instead of travelling as data.
const configHeader = `# Elite Dangerous Journal Monitor - configuration
#
# Restart the program after editing. / 修改后重启程序生效。
# Durations accept 30s / 5m / 1h. / 时间字段支持 30s / 5m / 1h 这类写法。
# timezone: UTC+8 (default) | UTC | auto | UTC+5:30   (see tz.go)
# enable_panel = false never opens a port; monitoring and push still run.
#   为 false 时完全不监听端口，只运行监控与推送。
# wxpusher: alerts go out through WxPusher (wxpusher.zjiecode.com), a
#   third-party push service - not an official WeChat API. Fill in app_token
#   and uid to enable alerts; leave both empty to monitor without push.
#   推送走第三方服务 WxPusher（非微信官方接口）；填 app_token + uid 才会推送，
#   两者留空则只监控。凭据只留在本机，请勿外传。
# language: auto (default, follows the system) | 中文 | English
#   默认 auto：按系统区域自动切换界面语言；重启生效 (restart to apply)
#
# Delete this file to regenerate it from the built-in defaults.
# 删掉本文件会按内置默认值重新生成一份。
`

// saveConfig writes config.toml: the documentation header followed by the
// marshalled config.
func saveConfig(path string, c Config) bool {
	body, err := toml.Marshal(c)
	if err != nil {
		return false
	}
	return os.WriteFile(path, append([]byte(configHeader), body...), 0o600) == nil
}

func parseDuration(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
		return d
	}
	return def
}

// portOf returns the ":port" part of a listen address. The "http://localhost{0}"
// templates expect exactly that: the config holds a bind address, not a URL for a
// browser, so gluing it onto "localhost" printed "http://localhost127.0.0.1:8088"
// for the LAN-off setting the README recommends.
//
// It lives here rather than in guicommon.go because the console build carries no
// gui tag and needs it too.
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return addr
}

// ------------------------------------------------------------------
// API response types
// ------------------------------------------------------------------

type AppStatus struct {
	LogFileName string `json:"log_file_name"`
	TotalKills  int    `json:"total_kills"`
	TotalBounty int64  `json:"total_bounty"` // includes mission rewards
	HourKills   int    `json:"hour_kills"`
	HourBounty  int64  `json:"hour_bounty"` // includes mission rewards

	// Mission-reward portion of the two bounty totals above, split out so the
	// panel can show the breakdown
	TotalMissionReward int64 `json:"total_mission_reward"`
	HourMissionReward  int64 `json:"hour_mission_reward"`

	// Human-readable stats window ("1 h" / "30 min"), shown directly on the
	// panel so the UI does not hardcode "last hour" after a config change
	StatWindowText string `json:"stat_window_text"`

	// Current UI language code ("zh" / "en"); the web panel picks its string
	// table from it.
	Lang string `json:"lang"`

	// Kill trend: kills aggregated into trendBucket cells over the recent
	// window, for the panel to plot. Counts only kills (Bounty events), not
	// mission rewards.
	KillTrend       []TrendPoint `json:"kill_trend"`
	TrendWindowText string       `json:"trend_window_text"`

	BountyRecords []BountyItem `json:"bounty_records"`
	ShipInfo      ShipData     `json:"ship_info"`
	MessageLines  []string     `json:"message_lines"`
	UpdatedAt     string       `json:"updated_at"`
	Error         string       `json:"error,omitempty"`

	// Mission progress: baselined on the last Missions event, then adjusted
	// incrementally by MissionAccepted / MissionCompleted / MissionRedirected.
	// done = ready to hand in + completed after redirect; total = current
	// mission count; active = still in progress.
	MissionDone   int `json:"mission_done"`
	MissionActive int `json:"mission_active"`
	MissionTotal  int `json:"mission_total"`
}

type BountyItem struct {
	TimeLocal string `json:"time_local"`
	Credits   int64  `json:"credits"`
	ShipType  string `json:"ship_type"`
	// Mission reward (MissionCompleted) rather than a kill bounty; the panel
	// uses this to pick its label
	IsMission bool `json:"is_mission"`
}

// TrendPoint is one point on the trend chart: kills and kill bounty in one cell.
type TrendPoint struct {
	TimeLocal string `json:"time_local"` // cell start, HH:MM
	Kills     int    `json:"kills"`      // kills in this cell; the panel plots it as a rate
	Bounty    int64  `json:"bounty"`     // kill bounty in this cell; not plotted, shown only in tooltips
	KillsHour int    `json:"kills_hour"` // kills in the 1-hour block containing this cell; plotted
}

type ShipData struct {
	Vessel          string  `json:"vessel"`
	ShipName        string  `json:"ship_name"`
	ShipIdent       string  `json:"ship_ident"`
	CargoUsed       int     `json:"cargo_used"`
	CargoMax        int     `json:"cargo_max"`
	FuelMainCurrent float64 `json:"fuel_main_current"`
	FuelMainMax     float64 `json:"fuel_main_max"`
	FuelReserve     float64 `json:"fuel_reserve"`
	FuelPercent     float64 `json:"fuel_percent"`
	InFighter       bool    `json:"in_fighter"`
}

// ------------------------------------------------------------------
// Journal events
// ------------------------------------------------------------------

type JournalEvent struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
	MissionID int64  `json:"MissionID"`

	ShieldsUp *bool           `json:"ShieldsUp"`
	Rewards   []BountyReward  `json:"Rewards"`
	Active    []MissionActive `json:"Active"`

	From               string `json:"From"`
	FromLocalised      string `json:"From_Localised"`
	Message            string `json:"Message"`
	MessageLocalised   string `json:"Message_Localised"`
	ScanType           string `json:"ScanType"`
	Victim             string `json:"Victim"`
	VictimLocalised    string `json:"Victim_Localised"`
	PilotNameLocalised string `json:"PilotName_Localised"`
	Target             string `json:"Target"`
	TargetLocalised    string `json:"Target_Localised"`

	Ship          string `json:"Ship"` // internal ID, e.g. independent_fighter
	ShipLocalised string `json:"Ship_Localised"`
	ShipName      string `json:"ShipName"`
	ShipIdent     string `json:"ShipIdent"`
	ShipID        int64  `json:"ShipID"` // ship instance ID; changes when switching ships, used to detect "this is not the previous ship"

	// ShipyardSwap (swapping at a shipyard) uses the ShipType / ShipType_Localised
	// field names, not Loadout's Ship / Ship_Localised; updateShip normalizes them.
	ShipType          string `json:"ShipType"`
	ShipTypeLocalised string `json:"ShipType_Localised"`

	Reward int64 `json:"Reward"` // mission payout for MissionCompleted (Credits)

	CargoCapacity int          `json:"CargoCapacity"`
	FuelCapacity  FuelCapacity `json:"FuelCapacity"`
}

type BountyReward struct {
	Faction string `json:"Faction"`
	Reward  int64  `json:"Reward"`
}

// MissionActive is one entry of the Missions event's Active array.
//
// Missions whose objective is done and only need handing in at a station
// (players call this "ready to hand in") always report Expires 0; missions
// still in progress carry a remaining time limit (Expires in seconds). So
// Expires==0 can be treated as "completed".
//
// MissionRedirected is indeed a completion signal (objective met, player sent
// to a hand-in station), but it cannot be counted unconditionally: after a
// disconnect and reconnect the game re-sends a full Missions list in the same
// journal, and that list already reflects every earlier completion. So only a
// redirect that arrives after the last list, for a mission that was still in
// progress (Expires>0) at that time, is a new completion and may be counted;
// everything else is ignored to avoid double counting after a reconnect.
type MissionActive struct {
	MissionID        int64  `json:"MissionID"`
	Name             string `json:"Name"`
	LocalisedName    string `json:"LocalisedName"`
	Expires          int64  `json:"Expires"`
	PassengerMission bool   `json:"PassengerMission"`
}

// FuelCapacity must accept both shapes:
//
//	LoadGame: "FuelCapacity": 0.0
//	Loadout : "FuelCapacity": {"Main":32.0,"Reserve":1.07}
//
// An early version parsed only the object form, so the whole LoadGame line
// failed to parse and was dropped, and ship data was never available. Do not
// remove this compatibility logic.
type FuelCapacity struct {
	Main    float64
	Reserve float64
}

func (f *FuelCapacity) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &f.Main) == nil {
		return nil
	}

	var obj struct {
		Main    float64 `json:"Main"`
		Reserve float64 `json:"Reserve"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}

	f.Main, f.Reserve = obj.Main, obj.Reserve
	return nil
}

func (e JournalEvent) reward() int64 {
	var sum int64
	for _, r := range e.Rewards {
		sum += r.Reward
	}
	return sum
}

// ------------------------------------------------------------------
// Cargo.json / Status.json
// ------------------------------------------------------------------

type CargoFile struct {
	Vessel string `json:"Vessel"` // "Ship" or "Suit" (on foot, Count is the backpack total)
	Count  int    `json:"Count"`
}

type StatusFile struct {
	Flags int64 `json:"Flags"`
	// Pointer to distinguish "no Fuel field" from "Fuel is 0"
	Fuel *Fuel `json:"Fuel"`
}

type Fuel struct {
	Main    float64 `json:"FuelMain"`
	Reserve float64 `json:"FuelReservoir"`
}

// ------------------------------------------------------------------
// Monitor
// ------------------------------------------------------------------

// monitor holds all state accumulated across ticks.
type monitor struct {
	// Data accumulated for the current journal; cleared when the journal
	// changes (i.e. re-login)
	bounties []bountyEvent
	messages []string

	// Incremental read position in the journal
	journalPath   string
	journalOffset int64

	// Static ship info, from Loadout / LoadGame
	ship shipInfo

	// Whether a historical journal has already been scanned to fill in
	// capacities. Historical journals never change, so one scan is enough;
	// otherwise several large files would be re-scanned every tick while in a
	// fighter.
	shipFilled bool

	// Known fighter model (recorded from LoadGame when logging in inside a
	// fighter). Deploying/recalling a fighter in combat writes no Loadout, so
	// this is the only fallback for the model; without it the generic
	// generic "Fighter" label is shown.
	fighterVessel string

	// Latest valid cargo / fuel values.
	// Kept from the previous tick while a file is being written or the player
	// is not in the mothership.
	cargoUsed int
	fuel      Fuel

	// Shield-drop push
	shieldInit bool
	lastPush   time.Time

	// Journal stall alerts
	lastActivity  time.Time // last time new journal content was read
	lastStallPush time.Time // last stall alert push time
	stallCount    int       // pushes so far; pushing stops at stallPushMax
	stallCapped   bool      // limit already reached and logged, to avoid logging every tick

	// Mission stats: baselined on the last Missions event, adjusted by
	// incremental events afterwards.
	// total = Active entries in the list + newly accepted - handed in/failed after
	// done  = entries with Expires==0 + missions turned completed by
	//         MissionRedirected after the list - those handed in afterwards
	// Each new Missions event rebuilds the baseline and clears the "after the
	// list" deltas, so only changes since the last list are counted; anything
	// earlier is already reflected by the list itself.
	//
	// All sets are keyed by MissionID: a repeated event for the same mission
	// (journal replay, duplicate game writes) neither inflates nor deflates the
	// counts.
	missionsTotal     int                // current mission total
	missionsDoneBase  int                // Expires==0 (ready to hand in) count in the last list
	missionDoneIDs    map[int64]struct{} // IDs of those missions, so they can be removed by ID
	missionInProgress map[int64]struct{} // still in progress (Expires>0 in the list + accepted later)
	missionRedirected map[int64]struct{} // received MissionRedirected after the list, counted as done
}

type shipInfo struct {
	Vessel      string
	ShipName    string
	ShipIdent   string
	ShipID      int64 // current ship ID, compared against event ShipID to detect a swap
	CargoMax    int
	FuelMainMax float64
}

// bountyEvent is one income entry: a Bounty event's payout or a MissionCompleted
// reward. mission=true means a mission reward — it counts toward credits only,
// never toward kills.
type bountyEvent struct {
	t        time.Time
	credits  int64
	shipType string
	mission  bool
}

type shieldDrop struct {
	t      time.Time
	vessel string
	text   string
}

func (m *monitor) run() {
	ticker := time.NewTicker(cfg.poll)
	defer ticker.Stop()

	for range ticker.C {
		m.safeTick()
	}
}

// safeTick isolates each tick.
// The recover must live inside the loop: with it in run()'s defer, a single
// panic would return from run(), kill the goroutine, and freeze the panel on
// its last frame forever.
func (m *monitor) safeTick() {
	defer func() {
		if r := recover(); r != nil {
			log.Println(T("log.monitor_panic", r))
			setStatus(AppStatus{
				Error:     T("log.monitor_error", r),
				UpdatedAt: fmtTime(time.Now().UTC()),
			})
		}
	}()

	setStatus(m.tick())
}

func (m *monitor) tick() AppStatus {
	st := AppStatus{StatWindowText: statWindowText(cfg.stat)}

	path, lines, reset := m.readJournal()

	if len(lines) > 0 { // new content: refresh the activity timestamp
		m.lastActivity = time.Now().UTC()
		// Activity resumed: reset the stall counter and cap flag, so the next
		// stall can alert stallPushMax more times.
		m.stallCount = 0
		m.stallCapped = false
	}

	if path == "" {
		st.Error = T("log.no_logfile")
	} else {
		st.LogFileName = filepath.Base(path)
		m.collect(&st, lines, reset)
	}

	m.checkStall(firstNonEmpty(st.LogFileName, T("common.none")))
	st.UpdatedAt = fmtTime(time.Now().UTC())

	return st
}

// collect parses the new lines and updates stats and ship state.
func (m *monitor) collect(st *AppStatus, lines []string, reset bool) {
	if reset { // re-login: previously accumulated data belongs to another session
		m.bounties, m.messages = nil, nil
		m.fuel, m.cargoUsed = Fuel{}, 0 // don't show the previous ship's fuel/cargo
		// Ship static info is void as well: the new session's LoadGame only
		// provides ship type / name / tank (and FuelCapacity as a bare number),
		// never cargo capacity. Keeping the previous session's values is not
		// just wrong on screen: it also makes shipData's "fill only when
		// capacity is missing" condition false, blocking that path entirely.
		m.ship = shipInfo{}
		m.fighterVessel = ""
		m.shipFilled = false // new journal: allow one more capacity backfill
		m.missionsTotal, m.missionsDoneBase = 0, 0
		m.missionDoneIDs = nil
		m.missionInProgress = nil
		m.missionRedirected = nil
	}

	var drop shieldDrop
	for _, line := range lines {
		evt, ok := decodeEvent(line)
		if !ok {
			continue
		}
		evTime, err := time.Parse(journalTimeLayout, evt.Timestamp)
		if err != nil {
			continue
		}
		m.handle(evt, evTime, &drop)
	}

	m.pushShieldDrop(drop)

	// Base the window on the real current time so it does not drift while idle
	cut := time.Now().UTC().Add(-cfg.stat)

	var totalBounty, hourBounty, totalMission, hourMission int64
	var totalKills, hourKills int

	for _, b := range m.bounties {
		totalBounty += b.credits
		if b.mission { // mission rewards count credits only, not kills
			totalMission += b.credits
		} else {
			totalKills++
		}
		if b.t.After(cut) {
			hourBounty += b.credits
			if b.mission {
				hourMission += b.credits
			} else {
				hourKills++
			}
		}
	}

	st.TotalKills = totalKills
	st.TotalBounty = totalBounty
	st.HourKills = hourKills
	st.HourBounty = hourBounty
	// Report mission rewards separately: merged into one total they hide the
	// breakdown
	st.TotalMissionReward = totalMission
	st.HourMissionReward = hourMission

	// Mission progress: baseline completions + those completed by redirect
	// after the list
	st.MissionDone = m.missionsDoneBase + len(m.missionRedirected)
	st.MissionTotal = m.missionsTotal
	st.MissionActive = m.missionsTotal - st.MissionDone
	if st.MissionActive < 0 {
		st.MissionActive = 0
	}

	for _, b := range tail(m.bounties, cfg.MaxListLen) {
		st.BountyRecords = append(st.BountyRecords, BountyItem{
			TimeLocal: fmtTime(b.t),
			Credits:   b.credits,
			ShipType:  b.shipType,
			IsMission: b.mission,
		})
	}
	// Copy before attaching to the snapshot: tail returns a sub-slice that
	// would share its backing array with m.messages. statusLock only guards
	// swapping the snapshot pointer, not the array contents — the monitor
	// goroutine appends without holding it. Today the writer happens to land
	// right of the read window, but that contract is too fragile; copying 200
	// string headers (~3.2KB) fully decouples them.
	st.MessageLines = append([]string(nil), tail(m.messages, cfg.MaxListLen)...)

	st.KillTrend, st.TrendWindowText = m.killTrend(time.Now().UTC())

	st.ShipInfo = m.shipData()
}

// killTrend aggregates in-memory kill events into fixed cells for the panel's
// trend chart. Only Bounty events count: mission rewards are one-off large
// sums and would spike the bounty line out of sync with the kill count.
//
// Range: from the cell of the first kill to the cell containing "now", with
// idle gaps kept as zero cells. The right end therefore always tracks the
// current time — after you stop, a run of zeros trails on the right, which is
// the truth. The last cell is the in-progress 10 minutes, so being lower than
// the cells to its left is normal.
//
// now is passed in by the caller (time.Now().UTC()) so tests can pin the time.
// The second return value is the span text (e.g. "3 h 20 min") shown in the
// front-end legend.
//
// This and fillWindows both assume m.bounties is appended in journal order, so
// timestamps are near-sorted (13 out-of-order spots in 387411 events across 431
// real logs, 0.003%). That lets us take the ends from both extremities and
// advance the sliding cursor only forward, keeping per-tick cost proportional
// to the window size with zero allocations. The price is that those 13 local
// inversions may land a kill in an adjacent cell — invisible in a 10-minute
// aggregate, but **never insert out-of-order data into m.bounties**.
func (m *monitor) killTrend(now time.Time) ([]TrendPoint, string) {
	bs, n := m.bounties, len(m.bounties)

	// Find the first and last kill from both ends instead of scanning the whole
	// list for the extremes
	first := 0
	for first < n && bs[first].mission { // the head may be a mission reward
		first++
	}
	last := n - 1
	for last >= 0 && bs[last].mission {
		last--
	}
	if first >= n { // no kills at all
		return nil, ""
	}

	// Align the start to a cell boundary so the whole curve does not shift
	// sideways on every refresh
	start := bs[first].t.Truncate(trendBucket)
	end := bs[last].t.Truncate(trendBucket)
	if cur := now.Truncate(trendBucket); cur.After(end) { // extend the end to now
		end = cur
	}
	if end.Before(start) { // in case the list was ever inserted in reverse: cells cannot be negative
		end = start
	}
	buckets := int(end.Sub(start)/trendBucket) + 1
	if buckets > maxTrendPoints { // safety net: for very long idle runs keep only the recent stretch
		start = end.Add(-time.Duration(maxTrendPoints-1) * trendBucket)
		buckets = maxTrendPoints
	}

	points := make([]TrendPoint, buckets)
	for i := range points {
		points[i].TimeLocal = fmtTime(start.Add(time.Duration(i) * trendBucket))[11:16]
	}

	m.fillWindows(now, start, points)

	return points, spanText(end.Add(trendBucket).Sub(start))
}

// fillWindows fills the two trend curves using rolling windows: the count inside
// the cell itself (Kills, which the panel extrapolates to a kill rate) and the
// count inside the rolling hour (KillsHour). Every window is
// [cell end - span, cell end): a normal cell is exactly itself, and the last cell
// is still running, so its end is clamped to "now" and the final point reads
// "last N minutes up to this moment" instead of dropping to 0 at every
// ten-minute mark and climbing back. The hour curve matches the panel's "last
// hour kills" — its rightmost value is the number shown on the panel.
func (m *monitor) fillWindows(now, start time.Time, points []TrendPoint) {
	bs, n := m.bounties, len(m.bounties)

	// A single cursor is enough: cell times increase, so the 1-hour window's
	// left edge increases too, meaning the "where does the window start" cursor
	// only moves forward — O(n) overall.
	//
	// Scan m.bounties directly instead of copying it out to sort — that step
	// produced 174KB of garbage per tick at 5000 entries (copy + sort.Slice)
	// for an identical result.
	lo := 0
	for i := range points {
		gs := start.Add(time.Duration(i) * trendBucket)
		e := gs.Add(trendBucket)
		if i == len(points)-1 && now.Before(e) && !now.Before(gs) { // last cell: clamp to now
			e = now
		}
		cut10, cut60 := e.Add(-trendBucket), e.Add(-trendRolling)

		// Advance the cursor to the first kill of the 1-hour window (mission
		// rewards are not plotted, so skip them too)
		for lo < n && (bs[lo].mission || bs[lo].t.Before(cut60)) {
			lo++
		}

		var k, hour int
		var cr int64
		for j := lo; j < n && bs[j].t.Before(e); j++ {
			b := bs[j]
			if b.mission { // mission rewards count credits not kills: neither line should move
				continue
			}
			hour++ // hour-average curve: last hour
			if !b.t.Before(cut10) {
				k++ // rate curve: this cell itself
				cr += b.credits
			}
		}
		points[i].Kills, points[i].Bounty, points[i].KillsHour = k, cr, hour
	}
}

// spanText renders a duration in human-readable form: "X h Y min" once it
// reaches an hour, otherwise minutes / seconds. The wording follows the UI language.
func spanText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= time.Hour:
		if m := int(d.Minutes()) % 60; m != 0 {
			return T("common.span_hours_min", int(d.Hours()), m)
		}
		return T("common.span_hours", int(d.Hours()))
	case d >= time.Minute:
		return T("common.span_min", int(d.Minutes()))
	default:
		return T("common.span_sec", int(d.Seconds()))
	}
}

// checkStall pushes an alert when the journal has no new records for a long
// time, and writes the same human-readable line to the panel.
// On startup it only records the baseline, so a pre-existing stall is not
// alerted retroactively; afterwards it alerts once per cfg.stall until updates
// resume. Alerts stop after stallPushMax pushes (and no more duplicate panel
// lines are added), so a long stall does not emit one alert per cfg.stall
// forever. Resumed activity zeroes the counter, giving the next stall another 3.
func (m *monitor) checkStall(logName string) {
	if m.lastActivity.IsZero() { // first scan: baseline only
		m.lastActivity = time.Now().UTC()
		return
	}

	idle := time.Since(m.lastActivity)
	if idle < cfg.stall {
		return
	}

	if !m.lastStallPush.IsZero() &&
		time.Since(m.lastStallPush) < cfg.stall {
		return
	}

	// Limit reached: stop pushing and stop adding duplicate panel lines
	// (resumed activity resets the counter).
	if m.stallCount >= stallPushMax {
		if !m.stallCapped {
			log.Print(T("log.stall_capped", stallPushMax))
			m.stallCapped = true
		}
		return
	}

	m.lastStallPush = time.Now().UTC()
	m.stallCount++
	dur := humanDuration(idle)

	// Also show it on the panel (at the same cadence as the push): plain text
	// with the current time for cross-reference
	stallLine := T("msg.stall_paused",
		fmtTime(time.Now().UTC()), dur)
	m.messages = append(m.messages, stallLine)

	// WxPusher push: one-line alert plus the last record time for troubleshooting
	text := T("wx.stall_body", dur, fmtTime(m.lastActivity))

	log.Println(T("log.stall_silent", dur))

	go sendWxPusher(T("wx.stall_title"), text)
}

// handle processes a single journal event.
func (m *monitor) handle(evt JournalEvent, evTime time.Time, drop *shieldDrop) {
	// msg appends a timestamped line. The body arrives already formatted:
	// translated text substitutes its {0}, {1}, ... placeholders inside T(), and
	// raw journal text is built with Sprintf at the call site. Do NOT run Sprintf
	// over a T() result - the templates carry no fmt verbs any more, so every
	// argument would be appended as "%!(EXTRA ...)".
	msg := func(body string) {
		m.messages = append(m.messages, fmtTime(evTime)+" | "+body)
	}

	switch evt.Event {

	case "LoadGame", "Loadout", "ShipyardSwap":
		m.updateShip(evt)

	case "Missions":
		// Every Missions event carries the full current mission list, so it
		// simply overwrites the baseline.
		m.missionsTotal = len(evt.Active)
		done := 0
		doneIDs := make(map[int64]struct{})
		inProg := make(map[int64]struct{})
		for _, a := range evt.Active {
			if a.Expires == 0 { // missions ready to hand in have no time limit
				done++
				doneIDs[a.MissionID] = struct{}{}
			} else {
				inProg[a.MissionID] = struct{}{}
			}
		}
		m.missionsDoneBase = done
		m.missionDoneIDs = doneIDs
		m.missionInProgress = inProg
		// New baseline: earlier "completed by redirect" counts are void; only
		// redirects after this list count
		m.missionRedirected = make(map[int64]struct{})

	case "MissionAccepted":
		// New mission accepted: total +1, treated as in progress (only a later
		// MissionRedirected marks it complete).
		if m.noteMissionAccepted(evt.MissionID) {
			m.missionsTotal++
		}

	case "MissionCompleted":
		// Handed in: the mission leaves the Active list, so total -1; it was
		// already counted as "ready to hand in", so done -1 as well.
		m.dropMission(evt.MissionID, true)

		// Mission payouts are income like kill bounties, so they feed the same
		// bounty stats (but never the kill count). Donation missions have
		// Reward 0 — no amount, so no blank log line.
		// The log only shows the mission-bounty label: mission names ("kill
		// pirates of faction X" etc.) are too long and crowd the line.
		if evt.Reward > 0 {
			m.bounties = append(m.bounties, bountyEvent{
				t:        evTime,
				credits:  evt.Reward,
				shipType: T("bounty.mission"),
				mission:  true,
			})
		}

	case "MissionFailed", "MissionAbandoned":
		// Failed / abandoned: also leaves the Active list, so total -1.
		// It was in progress, never counted as done, so done is unchanged.
		m.dropMission(evt.MissionID, false)

	case "MissionRedirected":
		// This redirect counts as a completion only if the mission was still in
		// progress at the last list. Missions already at Expires==0 (ready to
		// hand in) are not in inProgress and cannot be counted twice; missions
		// absent from the list entirely (leftovers from an old session) are not
		// counted either.
		if _, ok := m.missionInProgress[evt.MissionID]; ok {
			m.missionRedirected[evt.MissionID] = struct{}{}
		}

	case "ShieldState":
		// Must use break here, not return: return would exit handle entirely and
		// silently skip any logic added after this case
		if evt.ShieldsUp == nil || *evt.ShieldsUp {
			break
		}
		vessel := firstNonEmpty(m.ship.Vessel, T("bounty.unknown_ship"))
		text := T("bounty.shield_line",
			fmtTime(evTime), vessel)

		m.messages = append(m.messages, text)

		if evTime.After(drop.t) {
			drop.t, drop.vessel, drop.text = evTime, vessel, text
		}

	case "Bounty":
		reward := evt.reward() // computed once: needed by both the record and the timeline message
		m.bounties = append(m.bounties, bountyEvent{
			t:       evTime,
			credits: reward,
			// Target_Localised is sometimes missing (present in roughly 70% of
			// entries in a given log); fall back to the internal ID
			// (adder / cobramkiv) as before
			shipType: firstNonEmpty(evt.TargetLocalised, evt.Target, T("common.unknown")),
		})
		msg(T("bounty.kill_line",
			firstNonEmpty(evt.PilotNameLocalised, T("bounty.unknown_target")), reward))

	case "ReceiveText":
		// Player chat is raw journal text, not a translated template.
		msg(fmt.Sprintf("%s : %s",
			firstNonEmpty(evt.FromLocalised, evt.From),
			firstNonEmpty(evt.MessageLocalised, evt.Message)))

	case "Scanned":
		if evt.ScanType == "Cargo" {
			msg(T("msg.cargo_scanned"))
		}

	case "Kill":
		msg(T("msg.kill_target", firstNonEmpty(evt.VictimLocalised, evt.Victim)))

	case "FighterLaunched":
		msg(T("msg.fighter_deployed"))

	case "FighterDocked":
		msg(T("msg.fighter_docked"))

	case "FighterDestroyed":
		msg(T("msg.fighter_destroyed"))
	}
}

// noteMissionAccepted records a newly accepted mission and reports whether the
// total should be +1. Deduplicated by MissionID: the same mission reappearing
// (journal replay, etc.) is not counted twice.
func (m *monitor) noteMissionAccepted(id int64) bool {
	if id == 0 { // no ID means no dedup possible; count it directly
		return true
	}
	if _, ok := m.missionDoneIDs[id]; ok {
		return false
	}
	if _, ok := m.missionRedirected[id]; ok {
		return false
	}
	if m.missionInProgress == nil {
		m.missionInProgress = make(map[int64]struct{})
	}
	if _, ok := m.missionInProgress[id]; ok {
		return false
	}
	m.missionInProgress[id] = struct{}{}
	return true
}

// dropMission removes a mission from the stats — handing in, failing, or
// abandoning all take it out of the Active list.
// Total -1; whether done also drops depends on whether it was previously
// counted as done:
//   - in missionDoneIDs / missionRedirected: just remove it, done follows
//   - in no known set: only if assumeDone is true is it treated as "previously
//     done" and decremented too (MissionCompleted takes this path; failed /
//     abandoned missions were never counted as done, so nothing is subtracted)
//
// Counts are clamped at 0 so journal replays or out-of-sync lists cannot drive
// them negative.
func (m *monitor) dropMission(id int64, assumeDone bool) {
	if m.missionsTotal > 0 {
		m.missionsTotal--
	}
	delete(m.missionInProgress, id)

	if _, ok := m.missionDoneIDs[id]; ok { // was "ready to hand in"
		delete(m.missionDoneIDs, id)
		if m.missionsDoneBase > 0 {
			m.missionsDoneBase--
		}
		return
	}
	if _, ok := m.missionRedirected[id]; ok { // turned done by a redirect after the list
		delete(m.missionRedirected, id)
		return
	}
	// In no known set: most likely a mission that completed after the last list
	// but has no matching event in the journal
	if assumeDone && m.missionsDoneBase > 0 {
		m.missionsDoneBase--
	}
}

// updateShip refreshes static ship info from Loadout / LoadGame / ShipyardSwap.
// Empty values never overwrite existing data, so an incomplete event cannot
// zero out a valid value.
func (m *monitor) updateShip(evt JournalEvent) {
	s := &m.ship

	// ShipyardSwap uses ShipType / ShipType_Localised; normalize to the Loadout
	// field names first
	if evt.Ship == "" {
		evt.Ship, evt.ShipLocalised = evt.ShipType, evt.ShipTypeLocalised
	}

	// m.ship.Vessel holds the **mothership** model, so a fighter event must
	// never write to it:
	//   - logging back in while piloting a fighter makes LoadGame's Ship an SLF
	//     model (Independent_Fighter);
	//   - boarding a fighter from the mothership can also emit a Loadout with
	//     Ship=SLF.
	// Once written it can never be corrected: the VehicleSwitch event back to
	// the mothership carries **no ship type** (only To:"Mothership"), and it is
	// usually not followed by a new Loadout, leaving you flying an Anaconda
	// that displays as Taipan.
	//
	// Likewise a fighter has its own ShipID, so using it for the "ship swap"
	// check would wipe the mothership's capacity too. Hence the early return:
	// not a single mothership field is touched.
	if isFighter(evt.Ship) {
		m.fighterVessel = firstNonEmpty(evt.ShipLocalised, evt.Ship)
		return
	}

	// Ship-swap check: a different ShipID means a different ship, so the old
	// name / ident / capacity no longer apply. Not clearing leaks through —
	// real case: Anaconda (named GUAJI, 468 cargo) swapped for an unnamed
	// Krait Mk II (96 cargo); Loadout's ShipName is empty, "empty never
	// overwrites" preserved it, and the panel kept showing the old ship's name.
	if evt.ShipID != 0 && evt.ShipID != s.ShipID {
		s.ShipName, s.ShipIdent = "", ""
		s.CargoMax, s.FuelMainMax = 0, 0
		s.ShipID = evt.ShipID
	}

	s.Vessel = firstNonEmpty(evt.ShipLocalised, displayShipName(evt.Ship), s.Vessel)
	m.fighterVessel = "" // a real ship is flown now; the earlier fighter model is stale

	s.ShipName = firstNonEmpty(evt.ShipName, s.ShipName)
	s.ShipIdent = firstNonEmpty(evt.ShipIdent, s.ShipIdent)

	if evt.CargoCapacity > 0 {
		s.CargoMax = evt.CargoCapacity
	}
	if evt.FuelCapacity.Main > 0 {
		s.FuelMainMax = evt.FuelCapacity.Main
	}
}

// isFighter reports whether an ED internal ship type ID is a ship-launched
// fighter (SLF).
// Covers Independent_Fighter / Empire_Fighter / Federation_Fighter /
// GDN_Hybrid_Fighter and the like.
func isFighter(ship string) bool {
	return strings.Contains(strings.ToLower(ship), "fighter")
}

// shipData summarizes the current ship state.
func (m *monitor) shipData() ShipData {
	// Logging in inside a fighter leaves the current journal with no Loadout,
	// so capacity and mothership model can only come from historical logs.
	// Once per journal is enough — re-scanning several tens-of-MB files every
	// tick until the condition is met is pure waste.
	if !m.shipFilled && (m.ship.Vessel == "" || m.ship.CargoMax <= 0 || m.ship.FuelMainMax <= 0) {
		m.fillShipFromHistory()
		m.shipFilled = true
	}

	st, err := readStatus()

	// Fighters / SRVs have no main tank, so Status.json reports FuelMain 0.
	// It must not overwrite the mothership's fuel, or fuel would drop to zero
	// the moment you board a fighter.
	inFighter := st.Flags&(flagInFighter|flagInSRV) != 0
	if err == nil && st.Fuel != nil && !inFighter {
		m.fuel = *st.Fuel
	}

	// On foot, Cargo.json holds the backpack, not the cargo hold
	if cargo, err := readCargo(); err == nil && cargo.Vessel != "Suit" {
		m.cargoUsed = cargo.Count
	}

	fuelPct := 0.0
	if m.ship.FuelMainMax > 0 {
		fuelPct = m.fuel.Main / m.ship.FuelMainMax
	}

	// In a fighter / SRV the Status.json Flags have already flipped, so switch
	// the displayed vessel to what is currently being piloted. Uses the generic
	// label when the fighter model is unknown.
	vessel := firstNonEmpty(m.ship.Vessel, T("ship.waiting"))
	if inFighter {
		vessel = firstNonEmpty(m.fighterVessel, T("common.fighter"))
	}

	return ShipData{
		Vessel:          vessel,
		ShipName:        m.ship.ShipName,
		ShipIdent:       m.ship.ShipIdent,
		CargoUsed:       m.cargoUsed,
		CargoMax:        m.ship.CargoMax,
		FuelMainCurrent: m.fuel.Main,
		FuelMainMax:     m.ship.FuelMainMax,
		FuelReserve:     m.fuel.Reserve,
		FuelPercent:     fuelPct,
		InFighter:       inFighter,
	}
}

// pushShieldDrop pushes shield-drop notifications.
// The first scan only records the baseline; historical drops are not pushed.
func (m *monitor) pushShieldDrop(drop shieldDrop) {
	if !m.shieldInit {
		m.shieldInit = true
		m.lastPush = drop.t
		log.Println(T("log.shield_ready"))
		if !drop.t.IsZero() {
			log.Println(T("log.last_shield_drop", fmtTime(drop.t)))
		}
		return
	}

	if drop.t.IsZero() || !drop.t.After(m.lastPush) {
		return
	}

	// Update the time first so the next tick does not push again while the
	// request is in flight
	m.lastPush = drop.t
	log.Println(T("log.new_shield_drop", drop.text))

	go sendWxPusher(T("wx.shield_title"), T("wx.shield_down",
		fmtTime(drop.t), drop.vessel))
}

// ------------------------------------------------------------------
// Journal reading
// ------------------------------------------------------------------

// readJournal incrementally reads new lines from the current journal.
// The game only appends to the end, so the whole file need not be reparsed
// every tick. An empty path means no usable journal.
func (m *monitor) readJournal() (path string, lines []string, reset bool) {
	files, err := listJournals()
	if err != nil {
		return "", nil, false
	}
	path = files[0]

	if path != m.journalPath { // re-login produced a new file
		m.journalPath, m.journalOffset, reset = path, 0, true
		log.Println(T("log.new_session", filepath.Base(path)))
	}

	info, err := os.Stat(path)
	if err != nil {
		return path, nil, reset
	}

	if info.Size() < m.journalOffset { // truncated / recreated
		m.journalOffset, reset = 0, true
	}
	if info.Size() == m.journalOffset {
		return path, nil, reset
	}

	f, err := os.Open(path)
	if err != nil {
		return path, nil, reset
	}
	defer f.Close()

	if _, err := f.Seek(m.journalOffset, io.SeekStart); err != nil {
		return path, nil, reset
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return path, nil, reset
	}

	// Only process fully written lines; a half-written one waits for the next tick
	if n := len(data); n > 0 && data[n-1] != '\n' {
		data = data[:bytes.LastIndexByte(data, '\n')+1]
	}
	m.journalOffset += int64(len(data))

	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return path, lines, reset
}

// fillShipFromHistory backfills cargo / fuel capacity from earlier journals.
func (m *monitor) fillShipFromHistory() {
	files, err := listJournals()
	if err != nil {
		return
	}

	passed, scanned := false, 0

	for _, f := range files {
		if !passed { // skip the current journal and anything newer
			if f == m.journalPath {
				passed = true
			}
			continue
		}
		if scanned >= cfg.HistoryScanCount {
			return
		}
		scanned++

		if m.scanLoadout(f) {
			log.Print(T("log.ship_recovered",
				filepath.Base(f), m.ship.CargoMax,
				strconv.FormatFloat(m.ship.FuelMainMax, 'f', 1, 64)))
			return
		}
	}
}

// scanLoadout reads the last Loadout in one journal to backfill capacity, ship
// name, and mothership model.
//
// When logging back in while piloting a fighter, the current session has no
// mothership Loadout (VehicleSwitch back to the mothership carries no ship
// type), so the model can only be recovered here. It is written only when
// m.ship.Vessel is empty: a non-empty value means the current session has
// already seen the mothership, and history must not overwrite it.
func (m *monitor) scanLoadout(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	// Search backwards: the first hit is the session's last loadout, so the
	// whole file need not be read. Split on []byte to avoid string(data)
	// copying a tens-of-MB log.
	lines := bytes.Split(data, []byte("\n"))

	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"Loadout"`)) {
			continue
		}
		evt, ok := decodeEvent(string(lines[i]))
		if !ok || evt.Event != "Loadout" {
			continue
		}
		if isFighter(evt.Ship) {
			continue // this session's last loadout was a fighter; keep looking for the mothership
		}

		if evt.CargoCapacity > 0 {
			m.ship.CargoMax = evt.CargoCapacity
		}
		if evt.FuelCapacity.Main > 0 {
			m.ship.FuelMainMax = evt.FuelCapacity.Main
		}
		if m.ship.Vessel == "" {
			m.ship.Vessel = firstNonEmpty(evt.ShipLocalised, displayShipName(evt.Ship))
			// Record the ship ID: later Loadouts of the same ship must not be
			// treated as a ship swap and clear the data again
			m.ship.ShipID = evt.ShipID
		}
		m.ship.ShipName = firstNonEmpty(m.ship.ShipName, evt.ShipName)
		m.ship.ShipIdent = firstNonEmpty(m.ship.ShipIdent, evt.ShipIdent)
		return true
	}

	return false
}

// edShipDisplayNames maps internal ship type IDs to in-game display names.
//
// Only a fallback for missing Ship_Localised, which is more common than one
// would think: Loadout events **never** carry it (and that is the event that
// actually refreshes data after a swap), and ShipyardSwap has it for only some
// ships (the anaconda entry does not). Naive capitalization is not enough —
// "krait_mkii" would become "Krait_mkii".
var edShipDisplayNames = map[string]string{
	"adder":                   "Adder",
	"anaconda":                "Anaconda",
	"asp":                     "Asp Explorer",
	"asp_scout":               "Asp Scout",
	"belugaliner":             "Beluga Liner",
	"cobramkiii":              "Cobra Mk III",
	"cobramkiv":               "Cobra Mk IV",
	"cobramkv":                "Cobra Mk V",
	"corsair":                 "Corsair",
	"cutter":                  "Imperial Cutter",
	"diamondback":             "Diamondback Scout",
	"diamondbackxl":           "Diamondback Explorer",
	"dolphin":                 "Dolphin",
	"eagle":                   "Eagle",
	"empire_courier":          "Imperial Courier",
	"empire_eagle":            "Imperial Eagle",
	"empire_trader":           "Imperial Clipper",
	"explorer_nx":             "Caspian Explorer",
	"federation_assault_ship": "Federal Assault Ship",
	"federation_corvette":     "Federal Corvette",
	"federation_dropship":     "Federal Dropship",
	"federation_gunship":      "Federal Gunship",
	"fer_de_lance":            "Fer-de-Lance",
	"hauler":                  "Hauler",
	"independant_trader":      "Keelback",
	"krait_light":             "Krait Phantom",
	"krait_mkii":              "Krait Mk II",
	"lakonminer":              "Type-11 Prospector",
	"mamba":                   "Mamba",
	"mandalay":                "Mandalay",
	"mediumtransport01":       "Lynx Highliner",
	"orca":                    "Orca",
	"panthermkii":             "Panther Clipper Mk II",
	"python":                  "Python",
	"python_nx":               "Python Mk II",
	"sidewinder":              "Sidewinder",
	"type6":                   "Type-6 Transporter",
	"type7":                   "Type-7 Transporter",
	"type8":                   "Type-8 Transporter",
	"type9":                   "Type-9 Heavy",
	"type9_military":          "Type-10 Defender",
	"viper":                   "Viper Mk III",
	"viper_mkiv":              "Viper Mk IV",
}

// displayShipName turns an ED internal ship type ID into something readable
// (anaconda → Anaconda, krait_mkii → Krait Mk II). Falls back to capitalizing
// the first letter when the table has no entry, so it is at least not all
// lowercase.
func displayShipName(ship string) string {
	if ship == "" {
		return ""
	}
	if name, ok := edShipDisplayNames[strings.ToLower(ship)]; ok {
		return name
	}
	r := []rune(ship)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// decodeEvent leniently parses one journal line.
// A field with a mismatched type must not discard the whole line — as long as
// event / timestamp are parsed, keep going.
func decodeEvent(line string) (JournalEvent, bool) {
	var evt JournalEvent
	json.Unmarshal([]byte(line), &evt)
	return evt, evt.Event != "" && evt.Timestamp != ""
}

// listJournals returns journal paths sorted by session start, newest first.
func listJournals() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(cfg.journalDir(), "Journal.*.log"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no journal")
	}

	// Precompute the sort keys: computing them inside the comparator would
	// degrade to O(n log n) parses / Stat calls during the sort.
	keys := make(map[string]time.Time, len(files))
	for _, p := range files {
		keys[p] = journalTime(p)
	}

	sort.SliceStable(files, func(i, j int) bool {
		return keys[files[i]].After(keys[files[j]])
	})

	return files, nil
}

// journalTime parses the session start time from the filename. The session time
// in Journal.2026-09-09T074420.01.log is more reliable than the file mtime —
// copying, archiving, and time sync all scramble mtime — so mtime is only the
// fallback when parsing fails.
func journalTime(p string) time.Time {
	rest := strings.TrimPrefix(filepath.Base(p), "Journal.")
	if i := strings.Index(rest, "."); i > 0 {
		rest = rest[:i]
	}
	if t, err := time.Parse(sessionLayout, rest); err == nil {
		return t
	}
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// ------------------------------------------------------------------
// File I/O
// ------------------------------------------------------------------

func readJSON(name string, v any) error {
	data, err := os.ReadFile(filepath.Join(cfg.journalDir(), name))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func readStatus() (st StatusFile, err error) {
	err = readJSON("Status.json", &st)
	return
}

func readCargo() (c CargoFile, err error) {
	err = readJSON("Cargo.json", &c)
	return
}

// ------------------------------------------------------------------
// WxPusher
// ------------------------------------------------------------------

// wxClient needs its own timeout: http.Post's default client has none, so on a
// network failure the push goroutine would hang on the request forever.
var wxClient = &http.Client{Timeout: 10 * time.Second}

// wxSem caps in-flight push requests so an unreachable network cannot pile up
// goroutines without limit.
var wxSem = make(chan struct{}, 4)

func sendWxPusher(summary, content string) {
	// Without wxpusher app_token / uid configured, skip the pointless request
	// (startup already logs "push disabled")
	if !cfg.wxEnabled() {
		return
	}

	select {
	case wxSem <- struct{}{}:
		defer func() { <-wxSem }()
	default:
		log.Println(T("wx.too_many", summary))
		return
	}

	body, err := json.Marshal(map[string]any{
		"appToken":    cfg.WxPusher.AppToken,
		"content":     content,
		"summary":     summary,
		"contentType": 1,
		"uids":        []string{cfg.WxPusher.UID},
	})
	if err != nil {
		log.Println(T("wx.json_error", err))
		return
	}

	resp, err := wxClient.Post(cfg.WxPusher.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Println(T("wx.send_failed", err))
		return
	}
	defer resp.Body.Close()

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Println(T("wx.parse_failed", err))
		return
	}

	if result.Code != 1000 {
		log.Print(T("wx.send_failed_code", result.Code, result.Msg))
		return
	}

	log.Println(T("wx.push_ok", content))
}

// ------------------------------------------------------------------
// HTTP
// ------------------------------------------------------------------

func setStatus(st AppStatus) {
	statusLock.Lock()
	defer statusLock.Unlock()
	st.Lang = langCode() // language is frozen into the snapshot at publish time; the web panel reads it directly
	globalStatus = st
	atomic.AddUint64(&statusRev, 1)
}

// gzipResponseWriter routes everything written to it through gzip.Writer.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) { return w.gz.Write(p) }

// gzipPool reuses gzip.Writer so each request does not reallocate compression state.
var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// gzipHandler compresses responses for clients that advertise gzip support.
//
// The panel polls /api/status every 3 seconds, and repeated field names make up
// nearly 70% of that JSON (43 of 63 bytes per trend point are field names);
// compressed it shrinks to about one sixth. Browsers send Accept-Encoding and
// decompress automatically with fetch, so the front end needs no changes.
//
// Only /api/status is wrapped: static files go through http.FileServer, which
// sets Content-Length itself and supports Range — compression would break both,
// and those files are only a few KB anyway.
func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r) // client does not support it: return as-is
			return
		}

		gz := gzipPool.Get().(*gzip.Writer)
		defer gzipPool.Put(gz)
		gz.Reset(w)
		defer gz.Close()

		h := w.Header()
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding") // stop intermediate caches from serving the gzipped body to clients that cannot decode it
		h.Del("Content-Length")          // length changed; let net/http switch to chunked encoding

		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	statusLock.RLock()
	defer statusLock.RUnlock()

	_ = json.NewEncoder(w).Encode(globalStatus)
}

// i18nHandler serves the panel's whole string table: the base language overlaid
// by the active one, so the front end holds no translation of its own and adding
// a language touches only the Go tables.
//
// It is fetched once per page load (the language cannot change without a
// restart), which is why it is not gzip-wrapped like /api/status.
func i18nHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // keep CJK and the emoji tags readable
	if err := enc.Encode(struct {
		Lang  string            `json:"lang"`
		Tag   string            `json:"tag"`
		Table map[string]string `json:"table"`
	}{langCode(), langTag(), panelTable()}); err != nil {
		log.Println(T("log.panel_i18n_failed", err))
	}
}

func staticHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
		return
	}

	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// ------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------

// fmtTime converts a UTC time to a string in the display timezone (config.toml's
// "timezone", default UTC+8).
func fmtTime(t time.Time) string {
	return t.In(displayLocation()).Format(displayLayout)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return T("common.span_sec", int(d.Seconds()))
	case d < time.Hour:
		return T("common.span_min", int(d.Minutes()))
	default:
		return T("common.span_hours", strconv.FormatFloat(d.Hours(), 'f', 1, 64))
	}
}

// statWindowText renders the stats window for the panel ("1 h" / "30 min").
// Unlike humanDuration, which is for alerts and carries one decimal.
func statWindowText(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return T("common.span_hours", int(d.Hours()))
	case d >= time.Minute && d%time.Minute == 0:
		return T("common.span_min", int(d.Minutes()))
	default:
		return T("common.span_sec", int(d.Seconds()))
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func tail[T any](s []T, n int) []T {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ------------------------------------------------------------------

func main() {
	// Apply the language first: everything below is translated, including the
	// config-load and language-file messages buffered during package init (see
	// cfgNotices / langNotices). Language is fixed for the process lifetime -
	// config.toml requires a restart, it does not change at runtime.
	//
	// The configured value is often auto, which resolves against the system
	// locale right here; langNotice reports what it picked so a machine whose
	// locale is not translated can be told apart from a broken config.
	langNotice := applyLang()

	log.Println("Elite Dangerous Journal Monitor")
	log.Println(T("log.version", versionInfo()))
	if langNotice != nil {
		log.Println(langNotice())
	}
	for _, notice := range cfgNotices {
		log.Println(notice())
	}
	for _, notice := range langNotices {
		log.Println(notice())
	}
	log.Println(T("log.config_file", configPath()))
	log.Println(T("log.journal_dir", cfg.journalDir()))
	log.Print(T("log.scan_params",
		spanText(cfg.poll), spanText(cfg.stall), spanText(cfg.stat)))
	// The timezone must be set before the monitor goroutine starts: fmtTime only
	// reads it, and it is never changed afterwards.
	applyTimeZone(cfg.TimeZone)
	if !cfg.EnablePanel {
		log.Println(T("log.panel_off"))
	}

	if cfg.wxEnabled() {
		log.Println(T("wx.enabled"))
	} else {
		log.Println(T("wx.not_configured"))
	}

	go (&monitor{}).run()

	http.Handle("/api/status", gzipHandler(http.HandlerFunc(statusHandler)))
	http.HandleFunc("/api/i18n", i18nHandler)
	http.HandleFunc("/", staticHandler)

	// The rest is left to the build flavor; main is platform-independent here:
	//   GUI build (-tags gui) — the panel goes to the background, the main
	//     thread enters the window message loop and never returns
	//   console build        — prints the panel address and blocks on the HTTP server
	// Implementations live in gui.go and console.go.
	runUI()
}

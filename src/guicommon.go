//go:build gui

package main

// Shared logic layer for the GUI backends, build tag gui (OS-agnostic):
//   windows && gui                      -> the default Win32 build (gui.go / win32.go)
//   windows && gui && tk / linux && gui -> the Tk build (tk.go)
//
// Only window-system-independent logic lives here: turning AppStatus into display text,
// the runtime error state, and the shared log ring buffer (the Tk status tab shows the
// recent log lines from it).
// Anything touching window handles / Win32 calls stays in gui.go / win32.go; Tk-only code
// stays in tk.go.

import (
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

// Window title, shared by both GUI backends (follows the language).
// appTitle must be evaluated inside the function, not at package init: currentLang is
// only set by applyLang() during main startup, so at package init it is still the
// default and the title would stay Chinese in English mode.
func appTitle() string { return T("app.title") }

// ------------------------------------------------------------------
// Amount / text helpers
// ------------------------------------------------------------------

// commas adds thousands separators to an amount.
func commas(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// dash renders an empty value as an em dash; blank would look like a failed read.
func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// num formats with the web panel's precision; prec < 0 means "show all digits as-is"
// (the JS bare number).
func num(v float64, prec int) string {
	return strconv.FormatFloat(v, 'f', prec, 64)
}

// elide shortens over-long strings in the middle, keeping both ends — for paths the tail
// is the important part.
func elide(s string, max int) string {
	r := []rune(s)
	if len(r) <= max || max < 8 {
		return s
	}
	keep := max - 1
	head, tail := keep/2, keep-keep/2
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// ------------------------------------------------------------------
// Data panel text (copied from the web panel; don't get clever and change the wording)
// ------------------------------------------------------------------

// missionNote: the bounty total mixes in mission rewards, so show the breakdown to
// explain the number.
func missionNote(v int64) string {
	if v <= 0 {
		return ""
	}
	return T("common.mission_note", commas(v))
}

func buildSummary(st AppStatus) string {
	win := st.StatWindowText
	if win == "" {
		win = T("common.stat_window_1h")
	}

	lines := []string{
		T("summary.total_kills", st.TotalKills),
		T("summary.total_bounty", commas(st.TotalBounty)) + missionNote(st.TotalMissionReward),
		T("summary.span_kills", win, st.HourKills),
		T("summary.span_bounty", win, commas(st.HourBounty)) + missionNote(st.HourMissionReward),
	}
	if st.MissionTotal > 0 {
		lines = append(lines, T("summary.missions",
			st.MissionDone, st.MissionTotal, st.MissionActive))
	} else {
		lines = append(lines, T("summary.missions_none"))
	}
	return strings.Join(lines, "\n")
}

func buildShip(st AppStatus) string {
	s := st.ShipInfo
	if s.Vessel == "" && s.CargoMax == 0 && s.FuelMainMax == 0 {
		return T("summary.ship_unavailable") // like the web panel: a row of 0s would look like an empty tank
	}

	fighter := ""
	if s.InFighter {
		fighter = T("ship.in_fighter_inline")
	}

	lines := []string{T("ship.vessel", s.Vessel)}
	if s.ShipName != "" || s.ShipIdent != "" {
		lines = append(lines, T("ship.name", dash(s.ShipName), dash(s.ShipIdent))+fighter)
	} else if s.InFighter {
		lines = append(lines, T("ship.in_fighter"))
	}
	lines = append(lines,
		T("ship.cargo", s.CargoUsed, s.CargoMax),
		T("ship.main_fuel", num(s.FuelMainCurrent, 2), num(s.FuelMainMax, -1),
			num(s.FuelPercent*100, 1)),
		T("ship.reserve_fuel", num(s.FuelReserve, 2)),
	)
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------------
// Address / path / status line
// ------------------------------------------------------------------

// portOf extracts the port part (including the colon) from ":8088" / "0.0.0.0:8088".
// lanURL guesses a LAN-reachable address so a phone can open the web panel directly.
func lanURL() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		return "http://" + ipnet.IP.String() + portOf(cfg.ListenAddr)
	}
	return ""
}

// statusBarParts is the five shared bottom status-bar segments:
// HTTP state label / panel address (a link) / total kills / mission progress / total bounty.
//
// Shared by the Win32 and Tk builds so both UIs word the bar identically. The label and the
// address are separate cells because only the address is drawn as a link — blue, underlined and
// clickable. Kills, mission progress and bounty change while the program runs, so each GUI
// re-fills the bar on its own refresh tick instead of once at startup.
func statusBarParts(st AppStatus) [5]string {
	label, link := panelBarCells()
	return [5]string{
		0: label,
		1: link,
		2: T("status.total_kills", st.TotalKills),
		3: T("status.missions", st.MissionDone, st.MissionTotal),
		4: T("status.total_bounty", commas(st.TotalBounty)),
	}
}

// panelBarCells is the status bar's HTTP pair: the state label, and the address the panel can
// be opened at. The second is empty while the panel is off — there is no address to show, and
// the disabled state is already spelled out by the first.
func panelBarCells() (label, link string) {
	if !cfg.EnablePanel {
		return T("status.panel_off"), ""
	}
	return T("status.http_running"), panelURL()
}

// panelURL is where the panel is reachable on this machine. Built from portOf() so the
// displayed address always matches the bound port — never the raw bind address.
func panelURL() string {
	return "http://localhost" + portOf(cfg.ListenAddr)
}

// stateText builds the status-line text; the second result says whether to paint it red
// (error).
func stateText(st AppStatus) (string, bool) {
	if e := getRuntimeError(); e != "" {
		return T("status.prefix_error", e), true
	}
	if st.Error != "" {
		return T("status.prefix_warn", st.Error), true
	}

	parts := []string{T("status.monitoring")}
	if st.LogFileName != "" {
		parts = append(parts, T("status.current_log", st.LogFileName))
	}
	if st.UpdatedAt != "" {
		parts = append(parts, T("status.updated", st.UpdatedAt))
	}
	if st.TotalKills > 0 || st.TotalBounty > 0 {
		parts = append(parts,
			T("status.total_kills", st.TotalKills),
			T("status.total_bounty", commas(st.TotalBounty)))
	}
	if st.StatWindowText != "" {
		parts = append(parts, T("status.span_kills", st.StatWindowText, st.HourKills))
	}
	if st.MissionTotal > 0 {
		parts = append(parts, T("status.missions", st.MissionDone, st.MissionTotal))
	}
	return strings.Join(parts, " ｜ "), false
}

// ------------------------------------------------------------------
// Runtime errors (e.g. port in use) are kept separately and shown in red in the status bar.
// ------------------------------------------------------------------

var (
	runtimeErrMu sync.Mutex
	runtimeErr   string
)

func setRuntimeError(s string) {
	runtimeErrMu.Lock()
	runtimeErr = s
	runtimeErrMu.Unlock()
}

func getRuntimeError() string {
	runtimeErrMu.Lock()
	defer runtimeErrMu.Unlock()
	return runtimeErr
}

// ------------------------------------------------------------------
// Shared log ring buffer: the Linux status tab uses it to show recent log lines.
// startLogging (tk.go) writes into it; the UI thread reads via recentLogLines.
// ------------------------------------------------------------------

type logRing struct {
	mu      sync.Mutex
	lines   []string
	pending string
	max     int
}

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A line may arrive across several Writes: append to pending, cut out complete lines at
	// newlines, and keep a trailing partial line until the next Write completes it.
	r.pending += string(p)
	for {
		idx := strings.IndexByte(r.pending, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(r.pending[:idx], "\r") // tolerate \r\n
		r.push(line)
		r.pending = r.pending[idx+1:]
	}
	return len(p), nil
}

func (r *logRing) push(line string) {
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

// pending fallback: content from a Write not ending in a newline stays in pending and is
// committed with the next complete line.
var guiLogRing = &logRing{max: 800}

// recentLogLines returns the last n lines (old -> new) for display in the UI.
func recentLogLines(n int) []string {
	guiLogRing.mu.Lock()
	defer guiLogRing.mu.Unlock()
	src := guiLogRing.lines
	if n > 0 && len(src) > n {
		src = src[len(src)-n:]
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// snapshotStatus returns a read-only snapshot of the current status (shared by all GUI
// variants: Win32 / Gio / Tk).
func snapshotStatus() AppStatus {
	statusLock.RLock()
	defer statusLock.RUnlock()
	return globalStatus
}

// ensure io is referenced (logRing implements io.Writer via Write method).
var _ io.Writer = guiLogRing

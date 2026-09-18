//go:build windows && gui && !tk

package main

// GUI: a pure Win32 SDK window (no webview, no third-party libraries) with three tabs:
//
//	Runtime status — the program's parameters, status line, and log (replaces the old console window)
//	Data panel — what the web panel shows: summary / ship info / bounty log / events
//	Kill trend — the web panel's trend chart, drawn here with plain GDI (see drawTrend)
//
// Appearance comes from **native system visual styles**, no custom skinning: app.manifest
// declares Common-Controls 6.0, so Windows draws buttons / scrollbars / tabs / status bar in
// the current theme — Aero on Win7, flat on Win10/11 — matching the user's Personalization.
// Tabs use native SysTabControl32, the bottom status bar native msctls_statusbar32;
// switching tabs shows / hides whole groups of child controls (no property sheets or child
// dialogs, one less layer of nesting).
// The trend chart is the one thing here that is hand-drawn rather than a system
// control: WM_DRAWITEM on an owner-drawn static, with the row of GDI primitives at
// the top of win32.go. It shares its geometry with the Tk build (trendchart.go) and
// with the web panel, so all three look the same.
//
// On Windows `-tags gui` (no tk) uses this file; `-tags gui,tk` uses the Tk UI in tk.go.
// Both files provide startLogging / runUI, pick one against tk.go; on Linux only tk.go
// exists (no Win32).

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const (
	appClassName = "EliteMonWin32Class"
	// appToolbarClassName is the window class for the edge toolbar shown when the main window
	// is minimized with toolbar_edge set. A separate class (and wndProc) keeps the toolbar's
	// minimal message handling away from the main window's tab/owner-draw logic.
	appToolbarClassName = "EliteMonToolbarClass"

	// toolbarRadius is the corner radius of the edge toolbar (clipped with a rounded-rect
	// window region). toolbarInset pads the status text away from the rounded left/right ends;
	// the same value is used when measuring the bar width and when drawing, so the text never
	// hugs a corner and the padding stays symmetric.
	toolbarRadius int32 = 12
	toolbarInset  int32 = 14

	// Child control IDs
	idTabs    = 100 // native tab control (SysTabControl32)
	idInfo    = 101 // tab 1: program info box
	idState   = 102 // tab 1: status line
	idLogBox  = 103 // tab 1: runtime log
	idStatBox = 104 // tab 2: summary
	idShipBox = 105 // tab 2: ship info
	idBounty  = 106 // tab 2: bounty log
	idMsgBox  = 107 // tab 2: event messages
	idCapStat = 108 // the four captions on tab 2
	idCapShip = 109
	idCapCnt  = 110
	idCapMsg  = 111
	idTrend   = 112 // tab 3: the trend chart (owner-drawn static)
	idStatus  = 120

	// Status bar part indices (the bar is filled from statusBarParts). The address is its own
	// part because it is the only one that is drawn as a link and clicked.
	statusLinkPart = 1

	// Tabs
	tabStatus = 0
	tabData   = 1
	tabTrend  = 2

	winTimerID = 1

	// Menu command IDs (shared by the main window menu and the tray menu)
	cmdOpenConfig = 2001
	cmdOpenWeb    = 2003
	cmdClearLog   = 2004
	cmdExit       = 2005
	cmdAbout      = 2006
	cmdShowWindow = 2007

	// Tray icon callback message (offset from WM_APP so it can't collide with system messages)
	wmTray = 0x8000 + 1

	maxLogLines = 800
)

// tabLabels returns the tab strip text, in the same order as tabStatus /
// tabData / tabTrend.
// A function, not a package-level variable: T() cannot be evaluated during
// package init (the language tables register in init(), which runs after every
// variable initializer), so a var here would freeze the raw ids "tab.status" /
// "tab.data" into the tab strip.
//
// The third label reuses the web panel's chart caption rather than inventing a
// second name for the same thing.
func tabLabels() []string {
	return []string{T("tab.status"), T("tab.data"), T("panel.trend_title")}
}

// ------------------------------------------------------------------
// Log collection: in-memory ring buffer (feeds the GUI log list)
// ------------------------------------------------------------------

const (
	sevNormal = iota
	sevWarn
	sevError
)

type logSink struct {
	mu    sync.Mutex
	base  uint64 // sequence number of lines[0]
	next  uint64 // sequence number of the next entry
	lines []string
	sev   []int
}

var logBuf = &logSink{}

// startLogging takes over log output: into the in-memory ring buffer that feeds the GUI log
// list.
//
// Must be called before loadConfig — that runs during package init and logs things like
// "default config written / config parse failed", but this program has no console
// (-H windowsgui), so without this they go to a nonexistent stderr and are simply lost.
// The cfg initializer in elite_monitor.go calls this explicitly to enforce the ordering.
func startLogging() {
	if runningTests() {
		return // don't take over under test, or the test's own logging gets swallowed
	}
	log.SetOutput(logBuf)
}

// runningTests reports whether this is a go test binary.
// On Windows the test binary is xxx.test.exe, so checking for ".test" alone is not enough.
func runningTests() bool {
	base := strings.ToLower(filepath.Base(os.Args[0]))
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

func (s *logSink) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\r\n")

	s.mu.Lock()
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimRight(ln, "\r")
		s.lines = append(s.lines, sanitizeForGDI(ln))
		s.sev = append(s.sev, logSeverity(ln))
		s.next++
	}
	if n := len(s.lines) - maxLogLines; n > 0 {
		s.lines = append([]string(nil), s.lines[n:]...)
		s.sev = append([]int(nil), s.sev[n:]...)
		s.base += uint64(n)
	}
	s.mu.Unlock()

	return len(p), nil
}

// since returns the log lines after sequence number from; if from is too old (already
// dropped by the ring buffer) it starts at the oldest surviving line.
func (s *logSink) since(from uint64) ([]string, []int, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if from < s.base {
		from = s.base
	}
	if from > s.next {
		from = s.next
	}
	i := int(from - s.base)
	return s.lines[i:], s.sev[i:], s.next
}

// mark returns the sequence number of the next line, used to realign after "clear log".
func (s *logSink) mark() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// logSeverity classifies a log line for the log box colour. The keywords come
// from the active language table, so a newly added language keeps its own error
// wording highlighted; the emoji markers are language-independent.
func logSeverity(line string) int {
	errWords, warnWords := severityWords()
	switch {
	case containsAny(line, errWords):
		return sevError
	case containsAny(line, warnWords),
		strings.Contains(line, "⚠"), strings.Contains(line, "🚨"):
		return sevWarn
	}
	return sevNormal
}

// gdiEmoji replaces color emoji that GDI cannot draw with text markers.
// GDI has no color glyphs, so drawing them directly yields boxes.
//
// Note the lazy init: logs can be written during **package init** (the first log line from
// loadConfig goes straight to logBuf), when gui.go's package-level vars are not constructed
// yet — a package-level strings.Replacer would nil-panic there.
var (
	gdiEmojiOnce sync.Once
	gdiEmoji     *strings.Replacer
)

func emojiReplacer() *strings.Replacer {
	gdiEmojiOnce.Do(func() {
		gdiEmoji = strings.NewReplacer(
			"🚨", T("tag.shield"),
			"⚠️", T("tag.warn"),
			"⚠", T("tag.warn"),
			"📴", T("tag.silent"),
			"✅", T("tag.done"),
			"\ufe0f", "", // variation selector, drop it
		)
	})
	return gdiEmoji
}

func sanitizeForGDI(s string) string {
	s = emojiReplacer().Replace(s)
	if !strings.ContainsFunc(s, func(r rune) bool { return r > 0xFFFF }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s { // drop other non-BMP chars (mostly emoji) so they don't show as tofu boxes
		if r > 0xFFFF {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ------------------------------------------------------------------
// GUI state
// ------------------------------------------------------------------

type guiApp struct {
	hwnd        syscall.Handle
	font        syscall.Handle
	fontBold    syscall.Handle // bold, for panel captions (summary / ship info …)
	fontLink    syscall.Handle // underlined, for the status bar's panel address (drawn as a link)
	statusTheme syscall.Handle // theme handle for the status bar, opened lazily by drawLinkItem
	lineH       int32

	// The tab strip itself is the native control, no custom drawing; each page's
	// controls are still shown / hidden as a group. The page content rect is
	// computed on the fly from TCM_ADJUSTRECT in layout, never cached.
	tab   int            // current tab (tabStatus / tabData / tabTrend)
	tabs  syscall.Handle // SysTabControl32
	page1 []syscall.Handle
	page2 []syscall.Handle
	page3 []syscall.Handle

	// Tab 3
	trend      syscall.Handle
	lastTrend  trendKey // signature of the plotted data; repaint only when it moves
	trendHover int      // bucket under the cursor, -1 for none

	// Tab 1
	info   syscall.Handle
	state  syscall.Handle
	logBox syscall.Handle

	// Tab 2
	statBox syscall.Handle
	shipBox syscall.Handle
	bounty  syscall.Handle
	msgBox  syscall.Handle
	capStat syscall.Handle
	capShip syscall.Handle
	capCnt  syscall.Handle
	capMsg  syscall.Handle

	status  syscall.Handle
	lanAddr string // LAN address: computed once at startup, don't enumerate NICs every 500ms

	logNext   uint64
	lastInfo  string
	lastState string
	lastStat  [6]string
	stateErr  bool

	// Edge toolbar (toolbar_edge): a thin, topmost window docked to a screen edge that shows the
	// same status text as the bottom bar while the main window is minimized. It is a plain
	// self-painted window (no child controls), so mouse input lands on it directly: press-drag
	// moves it, double-click restores the main window, right-click opens the tray menu.
	// toolbarMode is true while it is showing; lastToolbar caches the joined text so it is only
	// repainted on change. toolbarDocked is true until the user drags the toolbar elsewhere;
	// while false the bar keeps its dragged position and is only resized as the status text
	// changes. toolbarX/Y remember the last placement so a plain click (no movement) does not
	// count as a drag.
	toolbar       syscall.Handle
	toolbarMode   bool
	toolbarDocked bool
	toolbarX      int32
	toolbarY      int32
	lastToolbar   string
	lastStatus    AppStatus // most recent snapshot, used to fill the toolbar the moment it appears

	lastSummary string
	lastShip    string
	bountyFeed  feedList
	msgFeed     feedList

	trayAdded    bool
	balloonShown bool
	nid          notifyIconDataW

	taskbarCreated uint32

	msg msgT // GetMessage wants a stable pointer, so keep it in the struct
}

// feedList is a "newest first" scrolling list: only new rows are inserted at the top, never a
// full rebuild.
//
// The backend hands out the last N entries (tail), so the window slides forward — "more
// entries than before" is not a reliable signal. Instead take the current first row and look
// for it in the new list: everything after it is new. If it is missing (session changed, or
// the list was cleared) rebuild the whole list. Rebuilding every 500ms would wipe the scroll
// position the user is reading and waste work on 200 rows.
type feedList struct {
	box    syscall.Handle
	newest string // text of the current first row (the newest one)
}

// feedRow is one row: text doubles as content and as the reconciliation key, data goes into
// LB_SETITEMDATA for the owner-draw code.
type feedRow struct {
	text string
	data uintptr
}

var app guiApp

var wndProcPtr = syscall.NewCallback(wndProc)
var toolbarWndProcPtr = syscall.NewCallback(toolbarWndProc)

// Only "semantic colors" are kept here: warning / error / bounty hues.
//
// Every other color is asked of the system: COLOR_BTNFACE for the window face, COLOR_WINDOW
// for list backgrounds, COLOR_WINDOWTEXT for body text — background and text color belong to
// the Windows theme so they follow the user's light / dark / high-contrast setting (custom
// skinning cannot do that).
// These few have no system-color equivalent yet must be told apart (the web panel colors them
// too); the values are dark for readability on a light background.
//
// GDI COLORREF is 0x00BBGGRR (not RGB!); the comments give #RRGGBB.
const (
	colWarn    = uint32(0x000055BB) // #BB5500 warning: dark orange
	colErr     = uint32(0x000000CC) // #CC0000 error: dark red
	colBounty  = uint32(0x00007A00) // #007A00 kill bounty: dark green
	colMission = uint32(0x00B06000) // #0060B0 mission bounty: dark blue

	// Trend chart curves. The web panel draws the same two series on a dark card
	// with bright #6f8 / #4cf; here the background is the system face colour, so
	// they are darkened to stay legible on it — same two hues, same pairing.
	colRateLine = uint32(0x002A7A0E) // #0E7A2A kill rate (10 min, extrapolated)
	colHourLine = uint32(0x00C05A00) // #005AC0 kills in the last hour

	// The status bar's panel address is a link, so it gets the conventional link look: blue
	// with an underline. Same blue as the chart's hour line, which keeps the palette to one
	// accent; the underline carries the "this is clickable" half of the signal.
	colLink = colHourLine
)

// ------------------------------------------------------------------
// Entry point
// ------------------------------------------------------------------

// runUI starts the panel service and enters the window message loop (never returns).
//
// The panel service runs in the background: the GUI build has no console, so its failure
// must not take down the whole program — the error shows up in the status line where the
// user will see it. The console build's same-named implementation is in console.go.
func runUI() {
	if cfg.EnablePanel {
		go func() {
			if err := http.ListenAndServe(cfg.ListenAddr, nil); err != nil {
				log.Println(T("log.panel_start_failed", err))
				setRuntimeError(T("log.panel_start_failed", err.Error()))
			}
		}()
	} else {
		// Panel disabled in config: don't even listen (use this when you want monitoring only, no open port)
		log.Println(T("log.panel_off_note"))
	}

	runGUI()
}

func runGUI() {
	// Windows only delivers messages to the OS thread that created the window.
	// Go goroutines get moved between threads, so without locking the message loop
	// never receives anything.
	runtime.LockOSThread()

	// GDI+ backs the trend chart's anti-aliased drawing. Start it once for the
	// process; the trend tab's owner-draw calls into it during the message loop,
	// so it must be alive before the window is shown.
	if err := gdiplusStartup(); err != nil {
		log.Println("GDI+ init failed:", err)
	}
	defer gdiplusShutdown()

	hInst := getModuleHandle()

	// Load common controls v6 (tabs / status bar) — must precede creating them.
	// Note: what actually enables "system visual styles" is the Common-Controls 6.0
	// dependency in app.manifest (linked into the exe via rsrc_windows_amd64.syso),
	// not this call. Without the manifest the process binds to comctl32 v5.82 and the
	// UI falls straight back to the Win95 classic look; build.sh asserts this at build
	// time, don't delete that check.
	initCommonControls()

	app.font = createUIFont()
	app.fontBold = createUIFontBold()
	app.fontLink = createUIFontLink()
	app.lineH = textHeight(app.font) + 2
	app.taskbarCreated = registerWindowMessage("TaskbarCreated")

	if !app.registerClass(hInst) {
		log.Println(T("log.win_class_reg_failed"))
		return
	}
	if !app.registerToolbarClass(hInst) {
		log.Println(T("log.win_class_reg_failed"))
		return
	}
	if !app.createWindow(hInst) {
		log.Println(T("log.win_create_failed"))
		return
	}

	app.createControls()
	app.setItemHeights()
	app.layout()
	app.refresh()

	app.addTray()
	setTimer(app.hwnd, winTimerID, 500)

	showWindow(app.hwnd, swShow)
	pUpdateWindow.Call(uintptr(app.hwnd))

	log.Println(T("log.gui_started"))

	// Message loop: exit when GetMessage returns 0 (WM_QUIT) or -1 (error)
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&app.msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&app.msg)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&app.msg)))
	}
}

func (a *guiApp) registerClass(hInst syscall.Handle) bool {
	// Icons come from exe resources (src/icon/app.ico, compiled into the .syso by rsrc along
	// with the manifest). Both sizes are loaded using the system icon metrics: the class HIcon
	// is the large icon for Alt+Tab / taskbar, HIconSm the small one for the title bar corner
	// and taskbar. Set both, or one spot silently keeps the default system icon.
	wc := wndClassExW{
		Style:         csHRedraw | csVRedraw,
		LpfnWndProc:   wndProcPtr,
		HInstance:     hInst,
		HIcon:         loadAppIcon(hInst, false),
		HCursor:       loadCursor(idcArrow),
		HbrBackground: syscall.Handle(colorButtonFace + 1), // +1: how system color indices are passed
		LpszClassName: utf16Ptr(appClassName),
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.HIconSm = loadAppIcon(hInst, true)
	if wc.HIconSm == 0 {
		wc.HIconSm = wc.HIcon
	}
	return registerClassEx(&wc) != 0
}

// registerToolbarClass registers the window class for the edge toolbar. It is a plain popup
// (no caption, no menu) that paints itself in WM_PAINT and has no child controls, so every
// mouse message lands on it directly; CS_DBLCLKS is set so a double-click arrives as
// WM_LBUTTONDBLCLK (restore the main window).
func (a *guiApp) registerToolbarClass(hInst syscall.Handle) bool {
	wc := wndClassExW{
		Style:         csHRedraw | csVRedraw | csDblClk,
		LpfnWndProc:   toolbarWndProcPtr,
		HInstance:     hInst,
		HCursor:       loadCursor(idcArrow),
		HbrBackground: syscall.Handle(colorButtonFace + 1),
		LpszClassName: utf16Ptr(appToolbarClassName),
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	return registerClassEx(&wc) != 0
}

func (a *guiApp) createWindow(hInst syscall.Handle) bool {
	// WS_CLIPCHILDREN: the tab body area is now painted by the main window itself (see the
	// flattening of the tab control in layout); with this flag erase-on-resize/repaint skips
	// the child controls, so there is less flicker.
	hwnd := createWindowEx(0, appClassName, appTitle(), wsOverlappedWindow|wsClipChildren,
		140, 90, 1000, 720, 0, buildMenu(), hInst)
	if hwnd == 0 {
		return false
	}
	a.hwnd = hwnd
	return true
}

func (a *guiApp) control(class, text string, style, exStyle uint32, id int) syscall.Handle {
	return createWindowEx(exStyle, class, text, style, 0, 0, 12, 12, a.hwnd, syscall.Handle(id), 0)
}

func (a *guiApp) createControls() {
	const base = wsChild | wsVisible

	// The LAN address is computed once (swapping NIC or IP is rare — just restart the app)
	a.lanAddr = lanURL()

	// Default to the "data panel" tab: the daily interest is kills/bounties/ships, the runtime
	// status is only for troubleshooting.
	a.tab = tabData

	// ---- Native tab control ----
	// Created first so it sits at the bottom of the z-order; the page controls are created
	// later and cover the blank area below the tab strip.
	// WS_CLIPSIBLINGS is mandatory for a tab control, without it sibling controls smear it.
	a.tabs = a.control("SysTabControl32", "",
		base|wsClipSiblings|tcsTabs|tcsFocusNever, 0, idTabs)
	labels := tabLabels()
	tabInsert(a.tabs, 0, labels[0])
	tabInsert(a.tabs, 1, labels[1])
	tabInsert(a.tabs, 2, labels[2])
	sendMessage(a.tabs, tcmSetCurSel, uintptr(a.tab), 0)

	// ---- Tab 1: runtime status ----
	// Text blocks never get WS_EX_CLIENTEDGE — that sunken border is exactly the Win9x look.
	// The modern look sits cleanly on the window face, layering via whitespace and font weight.
	a.info = a.control("STATIC", "", base|ssLeft, 0, idInfo)
	a.state = a.control("STATIC", T("app.init"), base|ssLeftNoWordWrap, 0, idState)

	a.logBox = a.control("LISTBOX", "",
		base|wsVScroll|wsBorder|lbsNoIntegralHgt|lbsOwnerDrawFixed|lbsHasStrings|lbsDisableNoScl,
		wsExClientEdge, idLogBox)

	// ---- Tab 2: data panel (what the web panel shows, minus the trend chart) ----
	a.statBox = a.control("STATIC", T("app.init"), base|ssLeft, 0, idStatBox)
	a.shipBox = a.control("STATIC", T("app.init"), base|ssLeft, 0, idShipBox)

	// Bounty list is owner-drawn (three columns + recoloring for mission bounties), the message
	// list is fine with default drawing
	a.bounty = a.control("LISTBOX", "",
		base|wsVScroll|wsBorder|lbsNoIntegralHgt|lbsOwnerDrawFixed|lbsHasStrings|lbsDisableNoScl,
		wsExClientEdge, idBounty)
	a.msgBox = a.control("LISTBOX", "",
		base|wsVScroll|wsBorder|lbsNoIntegralHgt|lbsDisableNoScl,
		wsExClientEdge, idMsgBox)

	// feedList operates the list through its own handle, so the handle must be wired up here:
	// miss it and sync() sends messages to window 0 and the list stays empty forever (the page
	// looks fine, just no content).
	a.bountyFeed.box = a.bounty
	a.msgFeed.box = a.msgBox

	a.capStat = a.caption(T("tab.summary"), idCapStat)
	a.capShip = a.caption(T("tab.ship"), idCapShip)
	a.capCnt = a.caption(T("tab.bounty_mission"), idCapCnt)
	a.capMsg = a.caption(T("tab.events"), idCapMsg)

	// ---- Tab 3: trend chart ----
	// One owner-drawn static covering the whole page: it draws its own grid, axis,
	// labels and curves in WM_DRAWITEM (drawTrend). One control instead of a dozen
	// means one rectangle to lay out, and no seams for the theme to paint white.
	a.trend = a.control("STATIC", "", wsChild|wsVisible|ssOwnerDraw, 0, idTrend)
	a.trendHover = -1 // no column under the cursor yet

	// ---- Native status bar ----
	// Segment widths depend on the window width, so they are set in layout. It is a common
	// control v6, so the theme paints its look.
	a.status = a.control("msctls_statusbar32", "", base|sbarsSizeGrip, 0, idStatus)

	// The two tabs' controls are grouped so switching shows / hides a whole group
	a.page1 = []syscall.Handle{a.info, a.state, a.logBox}
	a.page2 = []syscall.Handle{
		a.capStat, a.statBox, a.capShip, a.shipBox,
		a.capCnt, a.bounty, a.capMsg, a.msgBox,
	}
	a.page3 = []syscall.Handle{a.trend}

	pageCtl := append(append([]syscall.Handle{}, a.page1...), a.page2...)
	pageCtl = append(pageCtl, a.page3...)
	for _, h := range append(pageCtl, a.tabs, a.status) {
		sendMessage(h, wmSetFont, uintptr(a.font), 1)
	}
	// Captions use bold — hierarchy comes from font weight, not from drawn separators.
	for _, h := range []syscall.Handle{a.capStat, a.capShip, a.capCnt, a.capMsg} {
		sendMessage(h, wmSetFont, uintptr(a.fontBold), 1)
	}
}

// caption is a data-panel caption (the web panel's <h3>): text only, no border.
func (a *guiApp) caption(text string, id int) syscall.Handle {
	return a.control("STATIC", text, wsChild|wsVisible|ssLeftNoWordWrap, 0, id)
}

// setItemHeights sets the row height for the owner-drawn lists.
// Must run before adding items: LBS_OWNERDRAWFIXED ignores the font, the row height comes
// only from LB_SETITEMHEIGHT, and the default clips half a CJK line.
func (a *guiApp) setItemHeights() {
	for _, box := range []syscall.Handle{a.logBox, a.bounty} {
		sendMessage(box, lbSetItemHeight, 0, uintptr(a.lineH+3))
	}
}

// uiFontNames are the UI font candidates: YaHei preferred, SimSun as the last fallback.
// uiFontHeight is the character height in px (about 9pt, the standard system UI size):
// GDI takes it negated, and the GDI+ chart font uses it directly as its em size so
// the two draw at the same visual size.
const uiFontHeight = 12

var uiFontNames = []string{"Microsoft YaHei UI", "Microsoft YaHei", "SimSun"}

func createUIFont() syscall.Handle {
	for _, name := range uiFontNames {
		if f := createFont(-uiFontHeight, name); f != 0 {
			return f
		}
	}
	return 0
}

// createUIFontBold is the same size in bold, for the data-panel captions (summary / ship
// info …) — captions are set apart by font weight, not by drawn separators.
func createUIFontBold() syscall.Handle {
	for _, name := range uiFontNames {
		if f := createFontEx(-uiFontHeight, fwBold, name); f != 0 {
			return f
		}
	}
	return 0
}

// createUIFontLink is the same size and weight, underlined: the status bar's panel address is
// rendered as a link, and DrawText cannot underline by itself.
func createUIFontLink() syscall.Handle {
	for _, name := range uiFontNames {
		if f := createFontUnderline(-uiFontHeight, fwNormal, name); f != 0 {
			return f
		}
	}
	return 0
}

// ------------------------------------------------------------------
// Window procedure
// ------------------------------------------------------------------

func wndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	// TaskbarCreated is a runtime-registered message with no fixed value, so it must be
	// compared before the switch
	if app.taskbarCreated != 0 && msg == app.taskbarCreated {
		app.trayAdded = false // Explorer restarted, the icon is gone, re-add it
		app.addTray()
		return 0
	}

	switch msg {
	case wmCommand:
		app.onCommand(wParam, lParam)
		return 0

	case wmSysCommand:
		// The _ (minimize) button normally parks the window on the taskbar. With toolbar_edge
		// set, collapse it into the edge toolbar instead, so "minimize" and "close" both tuck
		// the app away the same way. Without toolbar_edge, fall through to the default minimise.
		if wParam&0xFFF0 == scMinimize && cfg.ToolbarEdge != "" {
			app.enterToolbarMode()
			return 0
		}
		return defWindowProc(hwnd, msg, wParam, lParam)

	case wmTimer:
		if wParam == winTimerID {
			app.refresh()
		}
		return 0

	case wmSize:
		if wParam != 1 { // 1 = SIZE_MINIMIZED
			app.layout()
		}
		return 0

	case wmNotify:
		// Tab change notification: the native tab control sends it via WM_NOTIFY, no hit
		// testing needed.
		// hdr.Code can be negative (TCN_SELCHANGE = -551), so compare it signed.
		hdr := ptrFromArg[nmhdr](lParam)
		if syscall.Handle(hdr.HwndFrom) == app.tabs && int32(hdr.Code) == tcnSelChange {
			app.onTabChange()
			return 0
		}
		// The status bar's address cell is a link: the app paints it blue and underlined
		// (statusBarSetOwnerDraw + drawLinkItem), so a click on it must open the panel.
		// NM_CLICK carries the clicked part index in its NMMOUSE payload, and part 1 is the
		// address. Mouse notifications keep arriving for an owner-drawn part, which is why the
		// click can stay on this path while the painting moved to WM_DRAWITEM.
		if syscall.Handle(hdr.HwndFrom) == app.status {
			if int32(hdr.Code) == nmClick {
				if ptrFromArg[nmmouse](lParam).DwItemSpec == statusLinkPart {
					app.openWeb()
				}
			}
		}
		return 0

	case wmDrawItem:
		dis := ptrFromArg[drawItemStruct](lParam)
		switch dis.CtlID {
		case idLogBox:
			return app.drawLogItem(dis)
		case idBounty:
			return app.drawBountyItem(dis)
		case idTrend:
			return app.drawTrend(dis)
		case idStatus:
			return app.drawLinkItem(dis)
		}

	case wmSetCursor:
		// Mouse movement shows up as WM_SETCURSOR rather than WM_MOUSEMOVE,
		// because the chart is a child control and the message belongs to
		// whichever window the cursor is over.
		//
		// wParam is deliberately ignored: it does not reliably name the chart's
		// control (the handle observed here is the frame's own), so the decision
		// comes from the cursor position against the chart's rectangle instead —
		// see hoverTrend. Returning 0 lets the arrow cursor be set.
		if app.overStatusLink() {
			// Over the link: show the hand and claim the message, otherwise the default
			// handling would put the arrow straight back (that is what returning 0 asks for).
			setCursor(loadCursor(idcHand))
			return 1
		}
		if app.tab == tabTrend {
			app.hoverTrend()
		}
		return 0

	case wmMouseMove, wmMouseLeave:
		// Over the frame itself, or the cursor has just left it (TME_LEAVE, armed
		// below): either way re-derive the readout from where the cursor now is,
		// which clears it unless it is still on the chart.
		if app.tab == tabTrend {
			app.hoverTrend()
			if msg == wmMouseMove {
				trackMouseLeave(app.hwnd) // one-shot: re-arm for the next exit
			}
		}
		return 0

	case wmCtlColorStatic:
		hdc := syscall.Handle(wParam)
		setBkMode(hdc, transparent)
		if syscall.Handle(lParam) == app.state && app.stateErr {
			setTextColor(hdc, colErr)
		} else {
			setTextColor(hdc, getSysColor(colorButtonText))
		}
		return uintptr(getSysColorBrush(colorButtonFace))

	case wmGetMinMaxInfo:
		mmi := ptrFromArg[minMaxInfo](lParam)
		mmi.PtMinTrackSize = pointT{X: 720, Y: 520}
		return 0

	case wmTray:
		switch uint32(lParam) {
		case wmLButtonDblClk, wmLButtonUp:
			app.restore()
		case wmRButtonUp:
			app.trayMenu()
		}
		return 0

	case wmClose:
		// Closing the window is not quitting: monitoring and WxPusher push keep running.
		// With toolbar_edge set, collapse to the edge toolbar instead of only the tray; the
		// tray icon is kept in both cases so the window is always recoverable.
		if cfg.ToolbarEdge != "" {
			app.enterToolbarMode()
		} else {
			app.hideToTray()
		}
		return 0

	case wmDestroy:
		app.removeTray()
		for _, f := range []syscall.Handle{app.font, app.fontBold, app.fontLink} {
			if f != 0 {
				deleteObject(f)
			}
		}
		closeThemeData(app.statusTheme)
		pPostQuitMessage.Call(0)
		return 0
	}

	return defWindowProc(hwnd, msg, wParam, lParam)
}

func (a *guiApp) onCommand(wParam, lParam uintptr) {
	id := int(wParam & 0xFFFF)

	if lParam != 0 {
		return // control notification: there are no buttons and the lists have no LBS_NOTIFY, ignore
	}

	switch id {
	case cmdOpenConfig:
		a.openConfig()
	case cmdOpenWeb:
		a.openWeb()
	case cmdClearLog:
		a.clearLog()
	case cmdShowWindow:
		a.restore()
	case cmdExit:
		a.quit()
	case cmdAbout:
		a.about()
	}
}

// drawLogItem owner-draws a log line: per-line colors (red errors, orange warnings) which a
// standard list box cannot do.
func (a *guiApp) drawLogItem(dis *drawItemStruct) uintptr {
	if int32(dis.ItemID) < 0 { // an empty list box sends a DRAWITEMSTRUCT with itemID = -1
		return 1
	}

	n := int(sendMessage(dis.HwndItem, lbGetTextLen, uintptr(dis.ItemID), 0))
	if n <= 0 {
		return 1
	}
	buf := make([]uint16, n+1)
	sendMessage(dis.HwndItem, lbGetText, uintptr(dis.ItemID), uintptr(unsafe.Pointer(&buf[0])))
	text := syscall.UTF16ToString(buf)

	sev := int(sendMessage(dis.HwndItem, lbGetItemData, uintptr(dis.ItemID), 0) & 0xFF)

	rc := dis.RcItem
	// No selection highlight: the list is read-only and "select the last row" is only a trick
	// to scroll to the bottom (LB_SETCURSEL); drawing a blue bar would look like a user
	// selection and mislead in a log window.
	fillRect(dis.HDC, &rc, getSysColorBrush(colorWindow))
	switch sev {
	case sevError:
		setTextColor(dis.HDC, colErr)
	case sevWarn:
		setTextColor(dis.HDC, colWarn)
	default:
		setTextColor(dis.HDC, getSysColor(colorWindowText))
	}

	setBkMode(dis.HDC, transparent)
	old := selectObject(dis.HDC, a.font)
	rc.Left += 6
	rc.Right -= 4
	drawText(dis.HDC, text, &rc, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)
	selectObject(dis.HDC, old)

	runtime.KeepAlive(buf)
	return 1
}

// drawBountyItem owner-draws a bounty row: time / amount (right-aligned) / ship type, with
// two vertical rules between the columns.
//
// Right-aligning the amount matches the web panel — left-aligned it looks ragged as the digit
// count changes, as if misaligned.
// Mission bounties (MissionCompleted) get a different color to set them apart from kill
// bounties; the third column already says "mission bounty", so color only reinforces.
func (a *guiApp) drawBountyItem(dis *drawItemStruct) uintptr {
	if int32(dis.ItemID) < 0 {
		return 1
	}

	n := int(sendMessage(dis.HwndItem, lbGetTextLen, uintptr(dis.ItemID), 0))
	if n <= 0 {
		return 1
	}
	buf := make([]uint16, n+1)
	sendMessage(dis.HwndItem, lbGetText, uintptr(dis.ItemID), uintptr(unsafe.Pointer(&buf[0])))
	text := syscall.UTF16ToString(buf)
	runtime.KeepAlive(buf)

	parts := strings.Split(text, "\t") // time \t amount \t ship type
	for len(parts) < 3 {
		parts = append(parts, "")
	}

	rc := dis.RcItem
	fillRect(dis.HDC, &rc, getSysColorBrush(colorWindow))

	const colTime = int32(126) // fits "2026-09-12 10:01:00" on one line
	const colAmt = int32(106)

	x0 := rc.Left + 6
	x1 := x0 + colTime
	x2 := x1 + colAmt
	vline(dis.HDC, x1+3, rc.Top+3, rc.Bottom-3)
	vline(dis.HDC, x2+3, rc.Top+3, rc.Bottom-3)

	setBkMode(dis.HDC, transparent)
	old := selectObject(dis.HDC, a.font)

	r := rc
	r.Left, r.Right = x0, x1
	setTextColor(dis.HDC, getSysColor(colorWindowText))
	drawText(dis.HDC, parts[0], &r, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)

	r = rc
	r.Left, r.Right = x1+6, x2
	if sendMessage(dis.HwndItem, lbGetItemData, uintptr(dis.ItemID), 0)&1 != 0 {
		setTextColor(dis.HDC, colMission)
	} else {
		setTextColor(dis.HDC, colBounty)
	}
	drawText(dis.HDC, parts[1], &r, dtRight|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)

	r = rc
	r.Left, r.Right = x2+6, rc.Right-4
	setTextColor(dis.HDC, getSysColor(colorWindowText))
	drawText(dis.HDC, parts[2], &r, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)

	selectObject(dis.HDC, old)
	return 1
}

// ------------------------------------------------------------------
// Tab 3: trend chart (owner-drawn static)
// ------------------------------------------------------------------

// hoverTrend re-derives which bucket the cursor is over and repaints if it moved.
// Called from WM_SETCURSOR, which is the only mouse traffic the parent sees while
// the cursor is over the chart's child control.
func (a *guiApp) hoverTrend() {
	pt := getCursorPos()
	screenToClient(a.trend, &pt)

	rc := getClientRect(a.trend)
	st := snapshotStatus()
	c := buildTrendChart(st.KillTrend, st.TrendWindowText, rc.Right, rc.Bottom)
	if col := c.ColumnAt(pt.X, pt.Y); col != a.trendHover {
		a.trendHover = col
		invalidateRect(a.trend, nil)
	}
}

// setTrend repaints the chart, and only when the plotted data actually moved.
//
// Nothing else affects what is drawn, so the whole refresh is one InvalidateRect:
// WM_DRAWITEM then re-reads the status and lays the chart out for whatever size
// the control currently is. Skipping the no-op case matters because this runs
// twice a second and repainting the control repaints its whole rectangle.
func (a *guiApp) setTrend(st AppStatus) {
	if sig := trendSignature(st); sig != a.lastTrend {
		a.lastTrend = sig
		invalidateRect(a.trend, nil)
	}
}

// drawTrend paints the trend chart into the static's rectangle: grid, shared
// kills-per-hour axis on the right, thinned clock labels along the bottom, and
// the two curves on top. It is the desktop twin of the web panel's renderTrend
// and shares its geometry with it and with Tk — see trendchart.go.
func (a *guiApp) drawTrend(dis *drawItemStruct) uintptr {
	rc := dis.RcItem
	// The chart is GDI+ (anti-aliased) now; build a graphics on the item's HDC. If
	// GDI+ is unavailable for some reason, fall back to a flat GDI fill so the tab
	// is never left transparent.
	g, ok := gpFromHDC(dis.HDC, rc.Right-rc.Left, rc.Bottom-rc.Top, rc.Left, rc.Top)
	if !ok {
		fillRect(dis.HDC, &rc, getSysColorBrush(colorButtonFace))
		return 1
	}
	defer g.Close()

	// Offscreen double-buffer draws in local (0,0) coordinates; the rare direct
	// fallback keeps the control-rect origin. rc.Left/Top are 0 for an owner-drawn
	// child control either way, so the chart lands in the same place.
	ox, oy := int32(0), int32(0)
	if !g.offscreen {
		ox, oy = rc.Left, rc.Top
	}
	st := snapshotStatus()
	c := buildTrendChart(st.KillTrend, st.TrendWindowText, rc.Right-rc.Left, rc.Bottom-rc.Top)

	// No system control paints the background, so lay it down first or the previous
	// frame smears.
	g.fillRect(ox, oy, rc.Right-rc.Left, rc.Bottom-rc.Top, gpNewBrush(argb(getSysColor(colorButtonFace))))

	font := gpNewFont(uiFontHeight)
	defer gpDeleteFont(font)

	if c.Empty {
		note := gpNewBrush(argb(getSysColor(colorGrayText)))
		g.text(c.Note, float32(ox+8), float32(oy+8),
			float32(rc.Right-rc.Left-16), float32(rc.Bottom-rc.Top-16),
			gpFmtCC, font, note)
		gpDeleteBrush(note)
		return 1
	}

	grid := gpNewBrush(argb(getSysColor(colorGrayText)))
	defer gpDeleteBrush(grid)
	gridPen := gpNewPen(argb(getSysColor(colorGrayText)), 1)
	defer gpDeletePen(gridPen)
	ratePen := gpNewPen(argb(colRateLine), 2)
	hourPen := gpNewPen(argb(colHourLine), 2)
	defer gpDeletePen(ratePen)
	defer gpDeletePen(hourPen)

	// Grid rows, each with its tick number in the right-hand column, vertically
	// centred on the line; the unit caption sits above the topmost one.
	for _, tk := range c.Ticks {
		g.line(ox, oy+tk.Y, ox+c.AxisX, oy+tk.Y, gridPen)
		g.text(tk.Text,
			float32(ox+c.AxisX+6), float32(oy+tk.Y)-float32(a.lineH)/2,
			float32(rc.Right-c.AxisX-8), float32(a.lineH),
			gpFmtNC, font, grid)
	}
	g.text(c.Unit,
		float32(ox+c.AxisX+6), float32(oy+2),
		float32(rc.Right-c.AxisX-8), float32(a.lineH),
		gpFmtNN, font, grid)

	// Clock labels, centred on their point, half-width limited so neighbours never
	// collide even at the thickest thinning.
	for _, lb := range c.Labels {
		g.text(lb.Text,
			float32(ox+lb.X-trendLabelHalfW), float32(oy+lb.Y),
			float32(trendLabelHalfW*2), float32(a.lineH),
			gpFmtCC, font, grid)
	}

	// Legend row along the top: a swatch per curve, then the covered span. All
	// three share one row and stop at the tick column: the unit caption lives
	// there, and right-aligning the span under it collided with it.
	ly := int32(4)
	lx := ox + int32(trendPadL/2)
	for _, item := range []struct {
		color uint32
		text  string
	}{{colRateLine, c.LegendRate}, {colHourLine, c.LegendHour}} {
		sw := gpNewBrush(argb(item.color))
		g.fillRect(lx, oy+ly+int32(float32(a.lineH)/2)-4, 10, 10, sw)
		gpDeleteBrush(sw)
		g.text(item.text,
			float32(lx+14), float32(oy+ly),
			float32(ox+c.AxisX-6-(lx+14)), float32(a.lineH+4),
			gpFmtNN, font, grid)
		// Advance by GDI+'s own measurement: GDI's idea of the width differs just
		// enough for the next swatch to land on top of this label's tail.
		lx += 14 + int32(g.measure(item.text, font, 10000).W+1) + 18
	}
	g.text(c.Span,
		float32(lx), float32(oy+ly),
		float32(ox+c.AxisX-6-lx), float32(a.lineH+4),
		gpFmtNN, font, grid)

	// The hour average first, so the more immediate rate curve lies on top of it.
	g.poly(c.Hour, ox, oy, hourPen)
	g.poly(c.Rate, ox, oy, ratePen)

	// Hover readout, on top of everything: a guide line down the bucket the cursor
	// is over plus a box with that bucket's numbers — the desktop form of the web
	// panel's <title> on each column.
	if hov := a.trendHover; hov >= 0 && hov < len(c.Cols) {
		col := c.Cols[hov]
		g.line(ox+col.X, oy+c.PlotTop, ox+col.X, oy+c.PlotBottom, gridPen)
		drawHoverTip(g, rc, c.HoverTip(hov), ox+col.X, oy+c.PlotTop, font)
	}
	return 1
}

// drawHoverTip draws the readout box through GDI+. The box is measured with the very
// font that draws it (GdipMeasureString at the shared wrap width), so its text can
// never outgrow it, then flipped to the other side of the guide line and clamped to
// the control so it stays readable at both ends of the chart.
func drawHoverTip(g *gpCanvas, rc rectT, tip string, anchorX, anchorY int32, font gpFont) {
	if tip == "" {
		return
	}
	m := g.measure(tip, font, trendTipW)
	bw, bh := int32(m.W)+14, int32(m.H)+10
	if bw <= 14 {
		return
	}
	bx := anchorX + 12
	if bx+bw > rc.Right-4 { // no room on the right: flip to the left of the line
		bx = anchorX - 12 - bw
	}
	if bx < rc.Left+4 {
		bx = rc.Left + 4
	}
	by := anchorY
	if by+bh > rc.Bottom-4 {
		by = rc.Bottom - 4 - bh
	}
	if by < rc.Top+4 {
		by = rc.Top + 4
	}

	bk := gpNewBrush(argb(getSysColor(colorInfoBk)))
	g.fillRect(bx, by, bw, bh, bk)
	gpDeleteBrush(bk)
	border := gpNewPen(argb(getSysColor(colorGrayText)), 1)
	g.drawRect(bx, by, bw, bh, border)
	gpDeletePen(border)

	tb := gpNewBrush(argb(getSysColor(colorInfoText)))
	g.text(tip,
		float32(bx+7), float32(by+5),
		float32(bw-14), float32(bh-10),
		gpFmtNN, font, tb)
	gpDeleteBrush(tb)
}

// vline draws a 1px vertical rule (a plain FillRect, no line-drawing API).
func vline(hdc syscall.Handle, x, top, bottom int32) {
	if bottom <= top {
		return
	}
	rc := rectT{Left: x, Top: top, Right: x + 1, Bottom: bottom}
	fillRect(hdc, &rc, getSysColorBrush(color3DShadow))
}

// ------------------------------------------------------------------
// Tabs
// ------------------------------------------------------------------

// onTabChange handles the native tab control's page-change notification
// (WM_NOTIFY / TCN_SELCHANGE).
func (a *guiApp) onTabChange() {
	sel := tabCurSel(a.tabs)
	if sel < 0 || sel == a.tab {
		return
	}
	a.tab = sel
	a.trendHover = -1 // the readout belongs to the tab we are leaving
	a.layout()        // layout ends with applyTab, which also fixes the new tab's layout
	a.refresh()       // sync immediately, don't wait for the next 500ms timer
}

// applyTab shows / hides the controls of the current tab as a group.
func (a *guiApp) applyTab() {
	show := func(handles []syscall.Handle, on bool) {
		cmd := int32(swHide)
		if on {
			cmd = swShow
		}
		for _, h := range handles {
			showWindow(h, cmd)
		}
	}
	show(a.page1, a.tab == tabStatus)
	show(a.page2, a.tab == tabData)
	show(a.page3, a.tab == tabTrend)
}

// ------------------------------------------------------------------
// Layout
// ------------------------------------------------------------------

func (a *guiApp) layout() {
	rc := getClientRect(a.hwnd)
	w, h := rc.Right, rc.Bottom
	if w <= 0 || h <= 0 {
		return
	}

	const gap = int32(6)
	const edge = int32(6) // margin between the tab control and the window edges

	// ---- Bottom status bar: height follows the font, segment widths follow the window ----
	sbarH := a.lineH + 8
	moveWindow(a.status, 0, h-sbarH, w, sbarH)
	// 6 segments: HTTP state label / panel address (drawn as a link) / total kills /
	// kills in the last hour / mission progress / total bounty. The first five are right
	// edges; -1 on the last means "extend to the right edge" (bounty, the widest, so it never
	// clips). Clamp so a very narrow window never produces a negative (inverted) segment — at
	// the 720px minimum the label and link simply shrink instead of breaking the layout.
	edges := []int32{w - 770, w - 610, w - 450, w - 300, w - 220, -1}
	prev := int32(0)
	for i := range edges {
		if edges[i] == -1 {
			continue
		}
		if edges[i] < prev+1 {
			edges[i] = prev + 1
		}
		if edges[i] > w {
			edges[i] = w
		}
		prev = edges[i]
	}
	statusBarSetParts(a.status, edges)
	// The panel address is painted by the app (blue + underlined link), so that one segment
	// switches the control into owner-draw mode. Idempotent, hence safe on every resize.
	statusBarSetOwnerDraw(a.status, statusLinkPart)

	// ---- Tab control ----
	// How the tab strip looks is the theme's business; the content area below it comes from
	// TCM_ADJUSTRECT — the strip height varies with theme and DPI, hardcoding it will misfit.
	tw, th := w-edge*2, h-sbarH-edge*2

	// First query the page content rect for the whole block: TCM_ADJUSTRECT depends only on how
	// tall the tab strip is (font / theme / DPI), not on the control's current height, so it
	// still locates the content after the control is flattened below.
	page := tabContentRect(a.tabs, rectT{0, 0, tw, th})

	// Keep the tab control only as tall as the tab strip, **do not** let it fill the block —
	// that is the root cause of the white gap stripes: with visual styles enabled the theme
	// fills everything below the strip with TABP_PANE **pure white #FFFFFF**, while the main
	// window and every text block use the system color COLOR_BTNFACE (#F0F0F0). Mismatched
	// backgrounds mean every seam between controls shows the white underneath, looking like
	// white stripes (measured: the 6px gap between the two columns and the 6px gap between
	// stats block and caption were both rgb(255,255,255)).
	// Flattening the control to just the strip hands the body area back to the main window's
	// class brush, so the whole page is uniformly #F0F0F0.
	// ⚠️ Don't change this back: the white stripes come right back.
	moveWindow(a.tabs, edge, edge, tw, page.Top)
	// The above uses the control's client coordinates, convert back to window coordinates
	x0 := page.Left + edge
	y0 := page.Top + edge
	pw := page.Right - page.Left
	pb := page.Bottom + edge

	// ---- Tab 1: runtime status ----
	y := y0

	// The info box height is measured from the actual text so a wrapped long path isn't cut off
	infoH := int32(0)
	if hdc := getDC(a.hwnd); hdc != 0 {
		infoH = measureText(hdc, a.font, a.lastInfo, pw-20)
		releaseDC(a.hwnd, hdc)
	}
	infoH += 10
	if infoH < a.lineH*3 {
		infoH = a.lineH * 3
	}

	moveWindow(a.info, x0, y, pw, infoH)
	y += infoH + 6

	stateH := a.lineH + 4
	moveWindow(a.state, x0+2, y, pw-4, stateH)
	y += stateH + 8

	logH := pb - y
	if logH < 48 {
		logH = 48
	}
	moveWindow(a.logBox, x0, y, pw, logH)

	// ---- Tab 2: data panel ----
	// Two rows of two: caption + content box / caption + list. Equal columns with a gap between.
	colW := (pw - gap) / 2
	capH := a.lineH + 2

	y = y0
	moveWindow(a.capStat, x0, y, colW, capH)
	moveWindow(a.capShip, x0+colW+gap, y, colW, capH)
	y += capH

	// Stats / ship boxes have a fixed height: the line count is stable, and a hardcoded height
	// means content changes don't trigger a re-layout (saves a pile of pointless MoveWindow).
	boxH := a.lineH*5 + 12
	moveWindow(a.statBox, x0, y, colW, boxH)
	moveWindow(a.shipBox, x0+colW+gap, y, colW, boxH)
	y += boxH + gap

	moveWindow(a.capCnt, x0, y, colW, capH)
	moveWindow(a.capMsg, x0+colW+gap, y, colW, capH)
	y += capH

	listH := pb - y
	if listH < 48 {
		listH = 48
	}
	moveWindow(a.bounty, x0, y, colW, listH)
	moveWindow(a.msgBox, x0+colW+gap, y, colW, listH)

	// ---- Tab 3: trend chart ----
	// It fills the page: the plot inside it re-lays itself out from whatever size
	// the control ends up with, so there is nothing here to measure.
	moveWindow(a.trend, x0, y0, pw, pb-y0)

	a.applyTab()
}

// measureText measures how tall the text needs to be at the given width (for the auto-sizing
// info box).
func measureText(hdc, hfont syscall.Handle, text string, width int32) int32 {
	if text == "" || width <= 0 {
		return 0
	}
	old := selectObject(hdc, hfont)
	r := rectT{Right: width}
	drawText(hdc, text, &r, dtCalcRect|dtWordBreak|dtNoPrefix)
	selectObject(hdc, old)
	return r.Bottom - r.Top
}

// measureTextWidth measures the single-line width of text in the given font — used to size the
// edge toolbar to its content so the bar is only as wide as the status text.
func measureTextWidth(hdc, hfont syscall.Handle, text string) int32 {
	if text == "" {
		return 0
	}
	old := selectObject(hdc, hfont)
	r := rectT{Right: 1 << 30}
	drawText(hdc, text, &r, dtCalcRect|dtSingleLine|dtNoPrefix|dtLeft)
	selectObject(hdc, old)
	return r.Right - r.Left
}

// ------------------------------------------------------------------
// Refresh
// ------------------------------------------------------------------

func (a *guiApp) refresh() {
	statusLock.RLock()
	st := globalStatus
	statusLock.RUnlock()

	a.setInfo(a.buildInfo())
	a.setState(st)
	a.pumpLog()
	a.setStatus(st)

	// Blocks are only refreshed while their tab is visible — no point feeding
	// lists or repainting a chart hidden behind another tab every 500ms. Switching
	// back re-runs refresh from layout, and the incremental sync fills in the rest.
	switch a.tab {
	case tabData:
		a.setSummary(st)
		a.setShip(st)
		a.pumpFeeds(st)
	case tabTrend:
		a.setTrend(st)
	}
}

func (a *guiApp) setInfo(text string) {
	if text == a.lastInfo {
		return
	}
	a.lastInfo = text
	setWindowText(a.info, text)
	a.layout() // line count changed, re-layout
}

func (a *guiApp) setState(st AppStatus) {
	text, isErr := stateText(st)
	if text == a.lastState && isErr == a.stateErr {
		return
	}
	a.lastState, a.stateErr = text, isErr
	setWindowText(a.state, text) // the text change repaints; the color comes from WM_CTLCOLORSTATIC
}

// setStatus refreshes the 4 status bar segments: HTTP state / panel address / total kills /
// total bounty.
// The text is shared with the Tk build (statusBarParts). The status bar is a native control
// updated per segment, and only segments that actually changed are rewritten — so the whole
// bar isn't repainted every 500ms.
func (a *guiApp) setStatus(st AppStatus) {
	a.lastStatus = st
	parts := statusBarParts(st)

	for i, t := range parts {
		if a.lastStat[i] == t {
			continue
		}
		a.lastStat[i] = t
		if i == statusLinkPart {
			// The address cell is owner-drawn, so there is no text to hand the control: the
			// painted string comes from lastStat (see drawLinkItem) and the cell only needs a
			// repaint. The item value does not change, so the control may not invalidate the
			// part by itself — do it here.
			invalidateRect(a.status, nil)
			continue
		}
		statusBarSetText(a.status, i, t)
	}

	// Mirror the same status into the edge toolbar while it is showing: join the segments into
	// one line and let WM_PAINT draw it (the toolbar is self-painted, no child status bar).
	// Only repaint when the text changed. Re-dock too, because a wider status line needs a
	// wider (still centred) bar. The leading spacer keeps the line clear of the rounded corner.
	if a.toolbarMode && a.toolbar != 0 {
		txt := joinStatus(parts[:])
		if txt != a.lastToolbar {
			a.lastToolbar = txt
			a.positionToolbar()
			invalidateRect(a.toolbar, nil)
		}
	}
}

// joinStatus turns the status segments into one line for the edge toolbar, skipping empty
// cells (e.g. the link is empty when the panel is off) and separating the rest with a bar.
func joinStatus(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("   |   ")
		}
		b.WriteString(p)
	}
	return b.String()
}

// overStatusLink reports whether the cursor sits on the status bar's address cell — the link.
// Like hoverTrend it hit-tests the cursor position rather than trusting WM_SETCURSOR's wParam,
// which does not reliably name the window under the cursor. An empty cell (panel off) is not a
// link, so it keeps the arrow.
func (a *guiApp) overStatusLink() bool {
	if a.status == 0 || a.lastStat[statusLinkPart] == "" {
		return false
	}
	rc, ok := statusBarPartRect(a.status, statusLinkPart)
	if !ok {
		return false
	}
	pt := getCursorPos()
	screenToClient(a.status, &pt)
	return pt.X >= rc.Left && pt.X < rc.Right && pt.Y >= rc.Top && pt.Y < rc.Bottom
}

// drawLinkItem paints the status bar's address cell — segment 1, the one part switched to
// owner-draw in layout. It is a link: blue and underlined, like the panel address on the data
// panel, and clicking it opens the panel (see the NM_CLICK branch in wndProc).
//
// The themed pane is drawn first so the cell is indistinguishable from the parts the control
// paints itself; without a theme (classic mode) it falls back to the button-face colour.
func (a *guiApp) drawLinkItem(dis *drawItemStruct) uintptr {
	// Identify the part by the sending control and the part index. DRAWITEMSTRUCT.CtlType is
	// useless here: there is no ODT_STATUSBAR (the ODT_* set is menu/listbox/combobox/button/
	// static/listview/tab/header), so a status bar leaves it at something that looks like another
	// control type. HwndItem is the status bar itself, which cannot be confused.
	if syscall.Handle(dis.HwndItem) != a.status || dis.ItemID != statusLinkPart {
		return 1
	}
	if a.statusTheme == 0 {
		a.statusTheme = openThemeData(a.status, "STATUS")
	}
	rc := dis.RcItem
	if !drawThemeBackground(a.statusTheme, dis.HDC, sppNormal, &rc) {
		fillRect(dis.HDC, &rc, getSysColorBrush(colorButtonFace))
	}

	url := a.lastStat[statusLinkPart]
	if url == "" { // panel off: background only, the cell is empty
		return 1
	}
	// Measured against the control's own rendering (SB_GETRECT + a screenshot): the theme insets
	// each part's text 3px from the part rect, and the next part's separator sits in the 2px
	// between rects. 2px here plus the ~1px left bearing of the first glyph lands the address on
	// the same column as the neighbouring cells — with 5px it sat 3px further right.
	rc.Left += 2
	rc.Right -= 4
	old := selectObject(dis.HDC, a.fontLink)
	setTextColor(dis.HDC, colLink)
	setBkMode(dis.HDC, transparent)
	drawText(dis.HDC, url, &rc, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)
	selectObject(dis.HDC, old)
	return 1
}

// The two text blocks on tab 2 have hardcoded heights, so content changes need no re-layout,
// just new text.

func (a *guiApp) setSummary(st AppStatus) {
	text := buildSummary(st)
	if text == a.lastSummary {
		return
	}
	a.lastSummary = text
	setWindowText(a.statBox, text)
}

func (a *guiApp) setShip(st AppStatus) {
	text := buildShip(st)
	if text == a.lastShip {
		return
	}
	a.lastShip = text
	setWindowText(a.shipBox, text)
}

// pumpFeeds syncs the bounty log / event messages into the two lists (newest on top, same as
// the web panel).
func (a *guiApp) pumpFeeds(st AppStatus) {
	limit := cfg.MaxListLen
	if limit <= 0 {
		limit = 200
	}

	rows := make([]feedRow, 0, len(st.BountyRecords))
	for _, r := range st.BountyRecords {
		var data uintptr
		if r.IsMission {
			data = 1
		}
		rows = append(rows, feedRow{text: bountyRow(r), data: data})
	}
	a.bountyFeed.sync(rows, limit)

	msgs := make([]feedRow, 0, len(st.MessageLines))
	for _, m := range st.MessageLines {
		// Messages may contain emoji ("🚨 护盾没了") that GDI cannot draw, so replace with text markers
		msgs = append(msgs, feedRow{text: sanitizeForGDI(m)})
	}
	a.msgFeed.sync(msgs, limit)
}

// sync brings the list content up to date with the new snapshot. rows is oldest first (the
// backend tail order), while the list shows newest first.
//
// Only new rows are inserted at the top: take the current first row and search the new list
// **backwards**; everything after the match is new. No match means rebuild the whole list
// (session changed / list was cleared / the window slid past).
func (f *feedList) sync(rows []feedRow, limit int) {
	if len(rows) == 0 {
		if int(sendMessage(f.box, lbGetCount, 0, 0)) > 0 {
			sendMessage(f.box, lbResetContent, 0, 0)
		}
		f.newest = ""
		return
	}

	if f.newest == "" {
		f.rebuild(rows, limit)
		return
	}

	i := -1
	for k := len(rows) - 1; k >= 0; k-- {
		if rows[k].text == f.newest {
			i = k
			break
		}
	}
	if i < 0 {
		f.rebuild(rows, limit)
		return
	}

	for k := i + 1; k < len(rows); k++ { // insert oldest to newest at index 0, so the newest lands on top
		insertTop(f.box, rows[k])
	}
	f.newest = rows[len(rows)-1].text
	f.trim(limit)
}

func (f *feedList) rebuild(rows []feedRow, limit int) {
	sendMessage(f.box, lbResetContent, 0, 0)
	// Insert oldest to newest at index 0: the last one inserted (the newest) ends up on top.
	// Never loop the other way — then a full first-time rebuild would put the **oldest on top**,
	// the opposite of sync's incremental inserts (the symptom is "bounty log in wrong order").
	for k := 0; k < len(rows); k++ {
		insertTop(f.box, rows[k])
	}
	f.newest = rows[len(rows)-1].text
	f.trim(limit)
}

// trim keeps only the newest limit rows — matching /api/status (tail) and the web panel.
func (f *feedList) trim(limit int) {
	for n := int(sendMessage(f.box, lbGetCount, 0, 0)); n > limit; n-- {
		sendMessage(f.box, lbDeleteString, uintptr(n-1), 0)
	}
}

func insertTop(box syscall.Handle, r feedRow) {
	p := utf16Ptr(r.text)
	idx := int(sendMessage(box, lbInsertString, 0, uintptr(unsafe.Pointer(p))))
	runtime.KeepAlive(p)
	if idx >= 0 && r.data != 0 {
		sendMessage(box, lbSetItemData, uintptr(idx), r.data)
	}
}

// pumpLog appends the new in-memory log lines to the list; the list keeps only the last
// maxLogLines lines.
func (a *guiApp) pumpLog() {
	lines, sevs, next := logBuf.since(a.logNext)
	if next == a.logNext {
		return
	}
	a.logNext = next

	for i, ln := range lines {
		p := utf16Ptr(ln)
		idx := int32(sendMessage(a.logBox, lbAddString, 0, uintptr(unsafe.Pointer(p))))
		runtime.KeepAlive(p)
		if idx >= 0 {
			sendMessage(a.logBox, lbSetItemData, uintptr(idx), uintptr(sevs[i]))
		}
	}

	if n := int(sendMessage(a.logBox, lbGetCount, 0, 0)) - maxLogLines; n > 0 {
		for i := 0; i < n; i++ {
			sendMessage(a.logBox, lbDeleteString, 0, 0) // delete from the head, keep the newest
		}
	}

	if n := int(sendMessage(a.logBox, lbGetCount, 0, 0)); n > 0 {
		sendMessage(a.logBox, lbSetCurSel, uintptr(n-1), 0) // select the last row = scroll to bottom
	}
}

func (a *guiApp) buildInfo() string {
	wx := T("about.push_disabled")
	if cfg.wxEnabled() {
		wx = T("about.enabled")
	}

	lan := ""
	if a.lanAddr != "" {
		lan = T("status.lan_pad", a.lanAddr)
	}

	lines := []string{
		T("about.lbl_config_file", elide(configPath(), 84)),
		T("about.lbl_journal_dir", elide(cfg.journalDir(), 84)),
		T("about.lbl_listen_addr", cfg.ListenAddr+lan),
		T("about.params",
			spanText(cfg.poll), spanText(cfg.stall), spanText(cfg.stat)),
		T("about.lbl_wx", wx),
	}
	return strings.Join(lines, "\r\n")
}

// bountyRow formats one bounty record into the three columns used by owner-draw:
// time \t amount \t ship type.
func bountyRow(r BountyItem) string {
	ship := r.ShipType
	if ship == "" {
		if r.IsMission {
			ship = T("bounty.mission")
		} else {
			ship = T("bounty.unknown_ship")
		}
	}
	return r.TimeLocal + "\t" + commas(r.Credits) + " Cr\t" + sanitizeForGDI(ship)
}

// ------------------------------------------------------------------
// Tray
// ------------------------------------------------------------------

func (a *guiApp) addTray() {
	if a.trayAdded {
		return
	}
	nid := notifyIconDataW{
		HWnd:             a.hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTray,
		HIcon:            loadAppIcon(getModuleHandle(), true),
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	copyUTF16(nid.SzTip[:], appTitle())

	if !shellNotifyIcon(nimAdd, &nid) {
		log.Println(T("log.tray_reg_failed"))
		return
	}
	a.nid, a.trayAdded = nid, true
}

func (a *guiApp) removeTray() {
	if !a.trayAdded {
		return
	}
	shellNotifyIcon(nimDelete, &a.nid)
	a.trayAdded = false
}

// balloon shows a balloon tip; only on the first minimize, so closing the window again
// doesn't nag.
func (a *guiApp) balloon(title, text string) {
	if !a.trayAdded {
		return
	}
	nid := a.nid
	nid.UFlags |= nifInfo
	nid.DwInfoFlags = niifInfo
	copyUTF16(nid.SzInfoTitle[:], title)
	copyUTF16(nid.SzInfo[:], text)
	shellNotifyIcon(nimModify, &nid)
}

func (a *guiApp) hideToTray() {
	if !a.trayAdded { // without a tray icon the window can never be found again, so just quit
		a.quit()
		return
	}
	if !a.balloonShown {
		a.balloonShown = true
		a.balloon(T("log.minimized"), T("gui.tray_tip"))
	}
	showWindow(a.hwnd, swHide)
}

func (a *guiApp) restore() {
	showWindow(a.hwnd, swShow)
	setForegroundWindow(a.hwnd)
	if a.toolbar != 0 {
		showWindow(a.toolbar, swHide)
		a.toolbarMode = false
	}
}

// enterToolbarMode collapses the main window into the edge toolbar: create it on first use,
// dock it to the configured screen edge, show it, and hide the main window. The tray icon is
// untouched, so the window is still recoverable from either the toolbar or the tray.
func (a *guiApp) enterToolbarMode() {
	if a.toolbar == 0 && !a.createToolbar() {
		// Toolbar window could not be created — fall back to the tray so the window is
		// never lost.
		a.hideToTray()
		return
	}
	// Every fresh entry into toolbar mode re-docks the bar to the configured edge, centred.
	// Dragging it afterwards (see toolbarWndProc) pins it wherever the user puts it.
	a.toolbarDocked = true
	a.positionToolbar()
	showWindow(a.toolbar, swShow)
	a.toolbarMode = true
	showWindow(a.hwnd, swHide)
	// Fill the toolbar from the last snapshot right away, not on the next 500ms tick.
	a.setStatus(a.lastStatus)
}

// createToolbar builds the edge-toolbar window. The window is a topmost, tool-window popup (no
// taskbar entry, no focus steal); it paints its own background and status text in WM_PAINT.
// Deliberately NO child controls: a child filling the bar would swallow the mouse input, so
// drag / double-click / right-click must land on the toolbar window itself.
func (a *guiApp) createToolbar() bool {
	if a.toolbar != 0 {
		return true
	}
	hInst := getModuleHandle()
	hwnd := createWindowEx(wsExTopMost|wsExToolWindow|wsExNoActivate,
		appToolbarClassName, "", wsPopup, 0, 0, 200, 24, 0, 0, hInst)
	if hwnd == 0 {
		return false
	}
	a.toolbar = hwnd
	return true
}

// positionToolbar docks the toolbar to the configured edge of the primary monitor's work area
// (SPI_GETWORKAREA already excludes the taskbar), so it never covers the taskbar. The bar is
// sized to its status text and centred horizontally, so it is a compact pill rather than a
// full-width strip that would hide other windows' title bar / close button. Its height matches
// the main window's status bar.
func (a *guiApp) positionToolbar() {
	if a.toolbar == 0 {
		return
	}
	barH := a.lineH + 8

	var wa rectT
	if !systemParametersInfo(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0) {
		wa = rectT{0, 0, getSystemMetrics(smCxScreen), getSystemMetrics(smCyScreen)}
	}
	wWork := wa.Right - wa.Left

	// Width follows the joined status text (+ symmetric padding) so the bar is only as wide as
	// it needs to be; clamp to 80% of the work area so a very long line still leaves the screen
	// edges clear.
	barW := int32(120)
	if hdc := getDC(a.toolbar); hdc != 0 {
		barW = measureTextWidth(hdc, a.font, joinStatus(a.lastStat[:])) + toolbarInset*2
		releaseDC(a.toolbar, hdc)
	}
	if barW > wWork*80/100 {
		barW = wWork * 80 / 100
	}
	if barW < 120 {
		barW = 120
	}

	var x, y int32
	if a.toolbarDocked {
		x = wa.Left + (wWork-barW)/2
		y = wa.Top
		if cfg.ToolbarEdge == "bottom" {
			y = wa.Bottom - barH
		}
	} else {
		// The user dragged the toolbar somewhere: keep its position, only resize it.
		r := getWindowRect(a.toolbar)
		x, y = r.Left, r.Top
	}
	moveWindow(a.toolbar, x, y, barW, barH)
	a.toolbarX, a.toolbarY = x, y

	// Round the corners. The toolbar is a frameless WS_POPUP, so clip it into a rounded rect via
	// a window region; child controls (the status bar) are clipped to the parent's region too.
	// The region is in window coordinates, so it must be rebuilt every time the bar is resized.
	// CreateRoundRectRgn's last two args are the corner ELLIPSE's bounding box (width/height),
	// so the corner radius is half of them: pass 2*radius.
	hrgn := createRoundRectRgn(0, 0, barW, barH, toolbarRadius*2, toolbarRadius*2)
	if !setWindowRgn(a.toolbar, hrgn, true) {
		deleteObject(hrgn)
	}
}

// toolbarWndProc is the edge toolbar's window procedure: press-drag moves the bar, double-click
// restores the main window, a right click opens the same menu as the tray icon; WM_COMMAND
// reuses the main menu handler. The bar is self-painted (no child controls). Destroying the
// toolbar does not quit the application.
func toolbarWndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCommand:
		app.onCommand(wParam, lParam)
		return 0
	case wmPaint:
		// Draw the pill: flat theme background (the rounded-rect window region clips the
		// corners) plus the joined status text, vertically centred and inset from both ends.
		var ps paintStruct
		hdc := beginPaint(hwnd, &ps)
		if hdc != 0 {
			rc := getClientRect(hwnd)
			fillRect(hdc, &rc, getSysColorBrush(colorButtonFace))
			if txt := app.lastToolbar; txt != "" {
				rc.Left += toolbarInset
				rc.Right -= toolbarInset
				selectObject(hdc, app.font)
				setBkMode(hdc, transparent)
				setTextColor(hdc, getSysColor(colorButtonText))
				drawText(hdc, txt, &rc, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)
			}
			endPaint(hwnd, &ps)
		}
		return 0
	case wmEraseBkgnd:
		return 1 // background is painted wholesale in WM_PAINT
	case wmLButtonDown:
		// Drag the toolbar anywhere on the screen: release the capture and re-dispatch the
		// press as a caption-bar click so the system moves the window (the classic way to drag
		// a frameless window). sendMessage blocks until the drag ends.
		releaseCapture()
		sendMessage(hwnd, wmNcLButtonDown, htCaption, 0)
		return 0
	case wmExitSizeMove:
		// A mouse move has just ended. Only treat it as a real drag if the window actually
		// moved (a plain click also enters the caption move loop and exits without moving):
		// then stop auto-docking so the toolbar stays where the user put it, even when the
		// status text changes and the bar is resized. It re-docks on the next entry into
		// toolbar mode.
		r := getWindowRect(hwnd)
		if r.Left != app.toolbarX || r.Top != app.toolbarY {
			app.toolbarDocked = false
		}
		return 0
	case wmLButtonDblClk:
		// Double-click the toolbar to bring the main window back. A single click is ignored on
		// purpose, so a stray click on the thin bar does not pop the window up by accident.
		app.restore()
		return 0
	case wmRButtonUp:
		app.trayMenu()
		return 0
	case wmDestroy:
		return 0
	}
	return defWindowProc(hwnd, msg, wParam, lParam)
}

func (a *guiApp) trayMenu() {
	m := createPopupMenu()
	if m == 0 {
		return
	}
	appendMenu(m, mfString, cmdShowWindow, T("gui.show_window_acc"))
	appendMenu(m, mfSeparator, 0, "")
	appendMenu(m, mfString, cmdOpenWeb, T("gui.open_web_panel_acc"))
	appendMenu(m, mfString, cmdOpenConfig, T("gui.open_config_file_acc"))
	appendMenu(m, mfSeparator, 0, "")
	appendMenu(m, mfString, cmdExit, T("gui.exit_acc"))

	pt := getCursorPos()
	setForegroundWindow(a.hwnd) // without this the menu won't dismiss when clicking outside it
	trackPopupMenu(m, pt.X, pt.Y, a.hwnd)
	postMessage(a.hwnd, wmNull, 0, 0)
	destroyMenu(m)
}

func (a *guiApp) quit() {
	a.removeTray()
	pDestroyWindow.Call(uintptr(a.hwnd))
}

// ------------------------------------------------------------------
// Menu commands
// ------------------------------------------------------------------

func buildMenu() syscall.Handle {
	main := createMenu()
	if main == 0 {
		return 0
	}

	file := createPopupMenu()
	appendMenu(file, mfString, cmdOpenConfig, T("gui.open_config_file_acc"))
	appendMenu(file, mfSeparator, 0, "")
	appendMenu(file, mfString, cmdExit, T("gui.exit_acc"))
	appendMenu(main, mfPopup, uintptr(file), T("gui.menu_file_acc"))

	view := createPopupMenu()
	appendMenu(view, mfString, cmdOpenWeb, T("gui.open_web_panel_acc"))
	appendMenu(view, mfSeparator, 0, "")
	appendMenu(view, mfString, cmdClearLog, T("gui.clear_log_acc"))
	appendMenu(main, mfPopup, uintptr(view), T("gui.menu_view_acc"))

	help := createPopupMenu()
	appendMenu(help, mfString, cmdAbout, T("gui.menu_about_acc"))
	appendMenu(main, mfPopup, uintptr(help), T("gui.menu_help_acc"))

	return main
}

func (a *guiApp) openConfig() {
	a.openPath(configPath())
}

func (a *guiApp) openWeb() {
	if !cfg.EnablePanel {
		messageBox(a.hwnd,
			T("gui.panel_disabled_msg"),
			T("gui.panel_disabled_title"), mbOK|mbIconInfo)
		return
	}
	shellExecute(panelURL())
}

// openPath opens with the system-associated program; on failure fall back to the containing
// folder.
func (a *guiApp) openPath(path string) {
	if shellExecute(path) {
		return
	}
	if !shellExecute(filepath.Dir(path)) {
		messageBox(a.hwnd, T("gui.cannot_open", path), T("gui.open_failed"), mbOK|mbIconError)
	}
}

// clearLog clears the window list only, leaving the in-memory buffer alone.
func (a *guiApp) clearLog() {
	sendMessage(a.logBox, lbResetContent, 0, 0)
	a.logNext = logBuf.mark()
	log.Println(T("gui.log_cleared"))
}

func (a *guiApp) about() {
	text := strings.Join([]string{
		appTitle(),
		T("about.lbl_version", versionInfo()),
		"",
		T("about.desc"),
		"",
		T("about.panel_url", portOf(cfg.ListenAddr)),
		T("about.lbl_config_file", configPath()),
		"",
		T("gui.close_hint"),
	}, "\r\n")
	messageBox(a.hwnd, text, T("gui.menu_about"), mbOK|mbIconInfo)
}

// ------------------------------------------------------------------
// Small helpers
// ------------------------------------------------------------------

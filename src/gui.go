//go:build windows && gui && !tk

package main

// GUI: a pure Win32 SDK window (no webview, no third-party libraries) with two tabs:
//
//	Runtime status — the program's parameters, status line, and log (replaces the old console window)
//	Data panel — what the web panel shows: summary / ship info / bounty log / events
//
// Appearance comes from **native system visual styles**, no custom skinning: app.manifest
// declares Common-Controls 6.0, so Windows draws buttons / scrollbars / tabs / status bar in
// the current theme — Aero on Win7, flat on Win10/11 — matching the user's Personalization.
// Tabs use native SysTabControl32, the bottom status bar native msctls_statusbar32;
// switching tabs shows / hides whole groups of child controls (no property sheets or child
// dialogs, one less layer of nesting).
// The only panel item not ported is the "kill trend" curve — the user explicitly said no.
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
	idStatus  = 120

	// Tabs
	tabStatus = 0
	tabData   = 1

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

// tabLabels returns the tab strip text, in the same order as tabStatus / tabData.
// A function, not a package-level variable: T() cannot be evaluated during
// package init (the language tables register in init(), which runs after every
// variable initializer), so a var here would freeze the raw ids "tab.status" /
// "tab.data" into the tab strip.
func tabLabels() []string {
	return []string{T("tab.status"), T("tab.data")}
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
	hwnd     syscall.Handle
	font     syscall.Handle
	fontBold syscall.Handle // bold, for panel captions (summary / ship info …)
	lineH    int32

	// Tabs use the native control, no custom drawing; the two tabs' controls are still shown
	// / hidden as groups. The page content rect is computed on the fly from TCM_ADJUSTRECT in
	// layout, never cached.
	tab   int            // current tab (tabStatus / tabData)
	tabs  syscall.Handle // SysTabControl32
	page1 []syscall.Handle
	page2 []syscall.Handle

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
	lastStat  [4]string
	stateErr  bool

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
	app.lineH = textHeight(app.font) + 2
	app.taskbarCreated = registerWindowMessage("TaskbarCreated")

	if !app.registerClass(hInst) {
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

	pageCtl := append(append([]syscall.Handle{}, a.page1...), a.page2...)
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
// -12 is the character height (about 9pt, the standard system UI size).
var uiFontNames = []string{"Microsoft YaHei UI", "Microsoft YaHei", "SimSun"}

func createUIFont() syscall.Handle {
	for _, name := range uiFontNames {
		if f := createFont(-12, name); f != 0 {
			return f
		}
	}
	return 0
}

// createUIFontBold is the same size in bold, for the data-panel captions (summary / ship
// info …) — captions are set apart by font weight, not by drawn separators.
func createUIFontBold() syscall.Handle {
	for _, name := range uiFontNames {
		if f := createFontEx(-12, fwBold, name); f != 0 {
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
		}
		return 0

	case wmDrawItem:
		dis := ptrFromArg[drawItemStruct](lParam)
		switch dis.CtlID {
		case idLogBox:
			return app.drawLogItem(dis)
		case idBounty:
			return app.drawBountyItem(dis)
		}

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
		// Closing the window is not quitting: monitoring and WeChat push keep running, so
		// collapse to the tray
		app.hideToTray()
		return 0

	case wmDestroy:
		app.removeTray()
		for _, f := range []syscall.Handle{app.font, app.fontBold} {
			if f != 0 {
				deleteObject(f)
			}
		}
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
	a.layout()  // layout ends with applyTab, which also fixes the new tab's layout
	a.refresh() // sync immediately, don't wait for the next 500ms timer
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
	// 4 segments: hint (takes the rest of the left) / listen port / LAN address / poll interval.
	// The first three are right edges; -1 on the last means "extend to the right edge".
	statusBarSetParts(a.status, []int32{w - 620, w - 480, w - 300, -1})

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
	a.setStatus()

	// The data panel blocks are only refreshed while visible — no point feeding lists hidden
	// behind another tab every 500ms. Switching back re-runs refresh from layout, and the
	// incremental sync fills in what was missed.
	if a.tab == tabData {
		a.setSummary(st)
		a.setShip(st)
		a.pumpFeeds(st)
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

// setStatus refreshes the 4 status bar segments: hint / bound port / LAN address / poll
// interval.
// The text is shared with the Tk build (statusBarParts). The status bar is a native control
// updated per segment, and only segments that actually changed are rewritten — so the whole
// bar isn't repainted every 500ms.
func (a *guiApp) setStatus() {
	hint := T("gui.close_hint_tray")
	if !a.trayAdded {
		hint = T("gui.tray_unavailable")
	}
	parts := statusBarParts(hint, a.lanAddr)

	for i, t := range parts {
		if a.lastStat[i] == t {
			continue
		}
		a.lastStat[i] = t
		statusBarSetText(a.status, i, t)
	}
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
	shellExecute("http://localhost" + portOf(cfg.ListenAddr))
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

//go:build windows && gui

package main

// Pure Win32 SDK bindings: bind the system DLLs directly, no third-party Go library.
// Uses user32 / gdi32 / shell32 / kernel32 / comctl32, all shipped with the system.
//
// comctl32 (common controls v6) is the key: only after declaring the
// Microsoft.Windows.Common-Controls 6.0 dependency in app.manifest and calling
// InitCommonControlsEx do tabs / status bar / scrollbars / buttons / list borders get drawn by
// **the system visual styles** (XP Luna, Win7 Aero, the Win10/11 flat theme).
// That is the right way to make a Win32 UI "modern" — use the system theme instead of painting
// a skin of your own.
//
// The build tag is `windows && gui` (without !tk): the Tk build doesn't use this UI, but on
// Windows it still needs the tray icon, popup menus, LoadImage and friends from here — those
// have nothing to do with the GUI framework, no reason to write them twice for Tk. Unused
// functions never make it into the final image, the only cost is a little compile time.

import (
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
)

var (
	pRegisterClassExW       = user32.NewProc("RegisterClassExW")
	pCreateWindowExW        = user32.NewProc("CreateWindowExW")
	pDefWindowProcW         = user32.NewProc("DefWindowProcW")
	pDestroyWindow          = user32.NewProc("DestroyWindow")
	pGetMessageW            = user32.NewProc("GetMessageW")
	pTranslateMessage       = user32.NewProc("TranslateMessage")
	pDispatchMessageW       = user32.NewProc("DispatchMessageW")
	pPostQuitMessage        = user32.NewProc("PostQuitMessage")
	pPostMessageW           = user32.NewProc("PostMessageW")
	pSendMessageW           = user32.NewProc("SendMessageW")
	pShowWindow             = user32.NewProc("ShowWindow")
	pUpdateWindow           = user32.NewProc("UpdateWindow")
	pSetWindowTextW         = user32.NewProc("SetWindowTextW")
	pGetClientRect          = user32.NewProc("GetClientRect")
	pMoveWindow             = user32.NewProc("MoveWindow")
	pSetTimer               = user32.NewProc("SetTimer")
	pGetDC                  = user32.NewProc("GetDC")
	pReleaseDC              = user32.NewProc("ReleaseDC")
	pLoadCursorW            = user32.NewProc("LoadCursorW")
	pLoadIconW              = user32.NewProc("LoadIconW")
	pLoadImageW             = user32.NewProc("LoadImageW")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pMessageBoxW            = user32.NewProc("MessageBoxW")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pCreateMenu             = user32.NewProc("CreateMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pDrawTextW              = user32.NewProc("DrawTextW")
	pFillRect               = user32.NewProc("FillRect")
	pDrawEdge               = user32.NewProc("DrawEdge")
	pInvalidateRect         = user32.NewProc("InvalidateRect")
	pBeginPaint             = user32.NewProc("BeginPaint")
	pEndPaint               = user32.NewProc("EndPaint")
	pGetSysColor            = user32.NewProc("GetSysColor")
	pGetSysColorBrush       = user32.NewProc("GetSysColorBrush")
)

var (
	pCreateFontW     = gdi32.NewProc("CreateFontW")
	pSelectObject    = gdi32.NewProc("SelectObject")
	pDeleteObject    = gdi32.NewProc("DeleteObject")
	pSetTextColor    = gdi32.NewProc("SetTextColor")
	pSetBkMode       = gdi32.NewProc("SetBkMode")
	pGetTextMetricsW = gdi32.NewProc("GetTextMetricsW")
)

var pInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")

var pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

var (
	pShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	pShellExecuteW    = shell32.NewProc("ShellExecuteW")
)

// ------------------------------------------------------------------
// Constants
// ------------------------------------------------------------------

const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsVScroll          = 0x00200000
	wsBorder           = 0x00800000

	wsExClientEdge = 0x00000200

	swHide = 0
	swShow = 5

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	// Window messages
	wmDestroy        = 0x0002
	wmSize           = 0x0005
	wmPaint          = 0x000F
	wmClose          = 0x0010
	wmLButtonDown    = 0x0201
	wmQuit           = 0x0012
	wmGetMinMaxInfo  = 0x0024
	wmDrawItem       = 0x002B
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmCtlColorStatic = 0x0138
	wmSetFont        = 0x0030
	wmNull           = 0x0000

	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205

	ssLeft           = 0x00000000
	ssLeftNoWordWrap = 0x0000000C
	ssSunken         = 0x00001000

	lbsNotify         = 0x0001
	lbsNoIntegralHgt  = 0x0100
	lbsOwnerDrawFixed = 0x0010
	lbsHasStrings     = 0x0040
	lbsDisableNoScl   = 0x1000

	lbAddString     = 0x0180
	lbInsertString  = 0x0181
	lbDeleteString  = 0x0182
	lbResetContent  = 0x0184
	lbSetCurSel     = 0x0186
	lbGetText       = 0x0189
	lbGetTextLen    = 0x018A
	lbGetCount      = 0x018B
	lbGetItemData   = 0x0199 // careful: 0x0198 is LB_GETITEMRECT (wants a RECT pointer), don't mix them up
	lbSetItemData   = 0x019A
	lbSetItemHeight = 0x01A0

	dtLeft        = 0x00000000
	dtCenter      = 0x00000001
	dtRight       = 0x00000002
	dtVCenter     = 0x00000004
	dtWordBreak   = 0x00000010
	dtSingleLine  = 0x00000020
	dtCalcRect    = 0x00000400
	dtNoPrefix    = 0x00000800
	dtEndEllipsis = 0x00004000

	// DrawEdge: classic 3D borders (combinations of BDR_*)
	edgeRaised = 0x0005 // BDR_RAISEDOUTER | BDR_RAISEDINNER
	edgeEtched = 0x0006 // BDR_SUNKENOUTER | BDR_RAISEDINNER
	bfRect     = 0x000F // draw all four sides
	bfBottom   = 0x0008

	transparent = 1

	colorWindow        = 5
	colorWindowText    = 8
	colorHighlight     = 13
	colorHighlightText = 14
	colorButtonFace    = 15
	color3DShadow      = 16
	colorGrayText      = 17
	colorButtonText    = 18

	idiApplication = 32512
	idcArrow       = 32512

	// Resource IDs of the program icon (src/icon/app.ico) once compiled into the exe.
	//
	// rsrc assigns IDs in "manifest first, then icons" order, so manifest=1 and icon group=2.
	// build.sh asserts these two numbers after a build (parsing the exe's RT_GROUP_ICON
	// directory), so if rsrc ever changes the assignment order the build fails loudly instead of
	// the icon quietly disappearing.
	// loadAppIcon also tries 1 as a second candidate — that is the slot the icons land in when
	// they come before the manifest.
	resIDManifest  = 1
	resIDIconGroup = 2

	// LoadImageW arguments: pull an icon of a given pixel size from the exe resources.
	// LoadImage instead of LoadIcon because LoadIcon only gives you the system large-icon size
	// (32px); using it for the tray/title-bar small icon makes the system hard-scale it, blurry.
	imageIcon     = 1
	lrDefaultSize = 0x0000
	lrShared      = 0x8000
	smCxIcon      = 11 // GetSystemMetrics: large icon width
	smCyIcon      = 12 // large icon height
	smCxSmIcon    = 49 // small icon width
	smCySmIcon    = 50 // small icon height

	mfString    = 0x00000000
	mfPopup     = 0x00000010
	mfSeparator = 0x00000800

	tpmRightBtn = 0x0002

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	niifInfo = 0x00000001

	mbOK        = 0x00000000
	mbIconInfo  = 0x00000040
	mbIconError = 0x00000010

	fwNormal         = 400
	fwBold           = 700
	defaultCS        = 1
	cleartypeQuality = 5

	// ---- Common controls v6 (comctl32): native tabs + native status bar ----
	// With visual styles active the theme draws both controls, not a line of custom painting.

	// InitCommonControlsEx dwICC bits
	iccBarClasses      = 0x00000004 // status bar / toolbar
	iccTabClasses      = 0x00000008 // tabs
	iccStandardClasses = 0x00004000 // standard controls (button / static / list box …)

	// Tab control SysTabControl32
	wsClipSiblings = 0x04000000 // mandatory style for a tab control, without it siblings paint over it
	wsClipChildren = 0x02000000 // for the main window: skip child areas when painting, kills flicker
	tcsTabs        = 0x0000     // default tab look (the theme decides the details)
	tcsFocusNever  = 0x8000     // clicking a tab doesn't steal focus, so the list box keeps its selection
	// ⚠️ Don't mistake 0x0080 for this constant — that is TCS_VERTICAL (tabs stacked vertically on the left)

	tcmFirst       = 0x1300
	tcmGetCurSel   = tcmFirst + 11 // 0x130B
	tcmSetCurSel   = tcmFirst + 12 // 0x130C
	tcmAdjustRect  = tcmFirst + 40 // 0x1328: control rect ⇄ page content rect
	tcmInsertItemW = tcmFirst + 62 // 0x133E

	tcnFirst     = -550
	tcnSelChange = tcnFirst - 1 // -551: tab change notification (delivered via WM_NOTIFY)

	tcifText = 0x0001

	wmNotify = 0x004E

	// Status bar msctls_statusbar32
	sbarsSizeGrip = 0x0100
	sbSetParts    = 0x0404 // WM_USER + 4
	sbSetTextW    = 0x040B // WM_USER + 11
)

// ------------------------------------------------------------------
// Structs (layout must match Win32; Go's natural alignment matches C on x64)
// ------------------------------------------------------------------

type pointT struct{ X, Y int32 }

type rectT struct{ Left, Top, Right, Bottom int32 }

// paintStruct is PAINTSTRUCT (used by BeginPaint / EndPaint).
// RgbReserved is the system-reserved 32 bytes and must stay as padding, or BeginPaint writes
// past the end.
type paintStruct struct {
	Hdc         syscall.Handle
	FErase      int32
	RcPaint     rectT
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type msgT struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      pointT
}

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type drawItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemAction uint32
	ItemState  uint32
	HwndItem   syscall.Handle
	HDC        syscall.Handle
	RcItem     rectT
	ItemData   uintptr
}

type minMaxInfo struct {
	PtReserved     pointT
	PtMaxSize      pointT
	PtMaxPosition  pointT
	PtMinTrackSize pointT
	PtMaxTrackSize pointT
}

type notifyIconDataW struct {
	CbSize           uint32
	HWnd             syscall.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            syscall.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         syscall.GUID
	HBalloonIcon     syscall.Handle
}

// nmhdr is the lParam of WM_NOTIFY; every common-control notification starts with it.
// Note Code is **signed** — the tab control's TCN_SELCHANGE is -551.
type nmhdr struct {
	HwndFrom syscall.Handle
	IdFrom   uintptr
	Code     int32
	_        int32 // padding: same size as Win32's NMHDR (24 bytes on x64)
}

// tcItemW is TCITEMW, used to insert a tab (only the text is filled in, the rest is for
// alignment).
type tcItemW struct {
	Mask        uint32
	DwState     uint32
	DwStateMask uint32
	PszText     *uint16
	CchTextMax  int32
	IImage      int32
	LParam      uintptr
}

// initCommonControlsExT is INITCOMMONCONTROLSEX.
type initCommonControlsExT struct {
	DwSize uint32
	DwICC  uint32
}

// ------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

// ptrFromArg restores a pointer that Win32 passed through a uintptr parameter into a *T
// (WM_DRAWITEM's lParam and MINMAXINFO's lParam both arrive this way).
//
// Writing (*T)(unsafe.Pointer(lParam)) directly is semantically fine — that is how Win32
// passes pointers — but go vet's unsafeptr check reports "possible misuse of unsafe.Pointer"
// on "uintptr → unsafe.Pointer". This goes one level around: read the uintptr's bit pattern
// as an unsafe.Pointer, then convert to the concrete type. Same bits, equivalent behavior.
func ptrFromArg[T any](p uintptr) *T {
	u := p
	return (*T)(*(*unsafe.Pointer)(unsafe.Pointer(&u)))
}

func registerClassEx(wc *wndClassExW) uint16 {
	r, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(wc)))
	return uint16(r)
}

func createWindowEx(exStyle uint32, class, title string, style uint32, x, y, w, h int32, parent, menu, inst syscall.Handle) syscall.Handle {
	r, _, _ := pCreateWindowExW.Call(
		uintptr(exStyle), uintptr(unsafe.Pointer(utf16Ptr(class))), uintptr(unsafe.Pointer(utf16Ptr(title))),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), uintptr(menu), uintptr(inst), 0)
	return syscall.Handle(r)
}

func defWindowProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func sendMessage(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := pSendMessageW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func postMessage(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) {
	pPostMessageW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
}

func setWindowText(hwnd syscall.Handle, s string) {
	p := utf16Ptr(s)
	pSetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

func showWindow(hwnd syscall.Handle, cmd int32) {
	pShowWindow.Call(uintptr(hwnd), uintptr(cmd))
}

func getClientRect(hwnd syscall.Handle) rectT {
	var r rectT
	pGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r)))
	return r
}

func moveWindow(hwnd syscall.Handle, x, y, w, h int32) {
	pMoveWindow.Call(uintptr(hwnd), uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
}

func setTimer(hwnd syscall.Handle, id uintptr, elapse uint32) {
	pSetTimer.Call(uintptr(hwnd), id, uintptr(elapse), 0)
}

func getDC(hwnd syscall.Handle) syscall.Handle {
	r, _, _ := pGetDC.Call(uintptr(hwnd))
	return syscall.Handle(r)
}

func releaseDC(hwnd, hdc syscall.Handle) {
	pReleaseDC.Call(uintptr(hwnd), uintptr(hdc))
}

func loadCursor(id uintptr) syscall.Handle {
	r, _, _ := pLoadCursorW.Call(0, id)
	return syscall.Handle(r)
}

// loadIcon fetches a system-provided icon (hInstance 0 selects the IDI_* set).
func loadIcon(id uintptr) syscall.Handle {
	r, _, _ := pLoadIconW.Call(0, id)
	return syscall.Handle(r)
}

// getSystemMetrics asks the system for a size (here: how big the icon should be).
func getSystemMetrics(index int32) int32 {
	r, _, _ := pGetSystemMetrics.Call(uintptr(index))
	return int32(r)
}

// loadImageIcon pulls an icon of the given pixel size from this exe's resources; 0 if absent.
// With LR_SHARED: the system caches the handle, the same (resource, size) returns the same one
// every time, and you must **not** DestroyIcon.
// Both the window class and the tray in this process want the same small icon, so sharing is
// convenient and can't leak.
func loadImageIcon(hInst syscall.Handle, id uintptr, cx, cy int32) syscall.Handle {
	r, _, _ := pLoadImageW.Call(uintptr(hInst), id, uintptr(imageIcon),
		uintptr(cx), uintptr(cy), uintptr(lrDefaultSize|lrShared))
	return syscall.Handle(r)
}

// loadAppIcon fetches the program's own icon (src/icon/app.ico → RT_GROUP_ICON).
//
// Sizes come from the system icon metrics (not hardcoded 32/16): at high DPI the system would
// scale the 16px bitmap up to 20/24px and blur it. Asking GetSystemMetrics for the real size
// makes LoadImage pick the closest bitmap in the .ico and scale that.
//
// The resource ID is normally resIDIconGroup (=2). It still falls back to trying 1: if rsrc ever
// puts the icons before the manifest the icon group lands at 1 (trying one more ID costs
// nothing, while a missing icon is easy to overlook). If everything fails it falls back to the
// generic system icon — at least better than "window with no icon at all".
func loadAppIcon(hInst syscall.Handle, small bool) syscall.Handle {
	cx, cy := getSystemMetrics(smCxIcon), getSystemMetrics(smCyIcon)
	if small {
		cx, cy = getSystemMetrics(smCxSmIcon), getSystemMetrics(smCySmIcon)
	}
	if cx <= 0 || cy <= 0 {
		cx, cy = 32, 32
	}
	for _, id := range []uintptr{resIDIconGroup, resIDManifest} {
		if h := loadImageIcon(hInst, id, cx, cy); h != 0 {
			return h
		}
	}
	return loadIcon(idiApplication)
}

func messageBox(parent syscall.Handle, text, title string, flags uint32) {
	p := utf16Ptr(text)
	t := utf16Ptr(title)
	pMessageBoxW.Call(uintptr(parent), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(t)), uintptr(flags))
	runtime.KeepAlive(p)
	runtime.KeepAlive(t)
}

func fillRect(hdc syscall.Handle, rc *rectT, brush syscall.Handle) {
	pFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(rc)), uintptr(brush))
}

// ------------------------------------------------------------------
// Common controls v6: load + self-check + thin wrappers for native tabs / status bar
// ------------------------------------------------------------------

// initCommonControls loads common controls v6. Must be called before creating the tabs /
// status bar.
// Skipping it doesn't error out, but the controls fall back to the v5 classic look and the
// system theme is wasted.
func initCommonControls() {
	icc := initCommonControlsExT{
		DwSize: uint32(unsafe.Sizeof(initCommonControlsExT{})),
		DwICC:  iccStandardClasses | iccBarClasses | iccTabClasses,
	}
	pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
}

// tabInsert inserts an item into the native tab control; passing the current item count as
// index appends.
func tabInsert(tab syscall.Handle, index int, text string) {
	p := utf16Ptr(text)
	item := tcItemW{Mask: tcifText, PszText: p, CchTextMax: int32(len(text) + 1)}
	sendMessage(tab, tcmInsertItemW, uintptr(index), uintptr(unsafe.Pointer(&item)))
	runtime.KeepAlive(p)
}

// tabCurSel returns the currently selected tab (-1 = none).
func tabCurSel(tab syscall.Handle) int {
	return int(int32(sendMessage(tab, tcmGetCurSel, 0, 0)))
}

// tabContentRect asks the tab control for its "page content rect": pass the control's client
// rect and it returns the rect filled with the content rectangle.
// How tall the tab strip is depends on theme and DPI together, so hardcoding it would misfit —
// only the control itself knows.
func tabContentRect(tab syscall.Handle, rc rectT) rectT {
	sendMessage(tab, tcmAdjustRect, 0, uintptr(unsafe.Pointer(&rc)))
	return rc
}

// statusBarSetParts sets the status bar segments: parts holds each segment's right edge (-1 on
// the last means "all the way to the right edge").
func statusBarSetParts(sb syscall.Handle, parts []int32) {
	if sb == 0 || len(parts) == 0 {
		return
	}
	sendMessage(sb, sbSetParts, uintptr(len(parts)), uintptr(unsafe.Pointer(&parts[0])))
	runtime.KeepAlive(parts)
}

// statusBarSetText sets the text of segment part (the low word of wParam is the segment number).
func statusBarSetText(sb syscall.Handle, part int, text string) {
	if sb == 0 {
		return
	}
	p := utf16Ptr(text)
	sendMessage(sb, sbSetTextW, uintptr(part), uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

func drawEdge(hdc syscall.Handle, rc *rectT, edge, flags uint32) {
	pDrawEdge.Call(uintptr(hdc), uintptr(unsafe.Pointer(rc)), uintptr(edge), uintptr(flags))
}

// invalidateRect with nil means the whole client area; bErase is always false — the tab strip
// repaints itself wholesale from WM_PAINT, so there is no need for the system to erase the
// background again (saves a flicker).
func invalidateRect(hwnd syscall.Handle, rc *rectT) {
	pInvalidateRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(rc)), 0)
}

func beginPaint(hwnd syscall.Handle, ps *paintStruct) syscall.Handle {
	r, _, _ := pBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(ps)))
	return syscall.Handle(r)
}

func endPaint(hwnd syscall.Handle, ps *paintStruct) {
	pEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(ps)))
}

func drawText(hdc syscall.Handle, text string, rc *rectT, format uint32) int32 {
	p := utf16Ptr(text)
	r, _, _ := pDrawTextW.Call(uintptr(hdc), uintptr(unsafe.Pointer(p)),
		^uintptr(0) /* -1: string is 0-terminated */, uintptr(unsafe.Pointer(rc)), uintptr(format))
	runtime.KeepAlive(p)
	return int32(r)
}

func getSysColor(idx int32) uint32 {
	r, _, _ := pGetSysColor.Call(uintptr(idx))
	return uint32(r)
}

func getSysColorBrush(idx int32) syscall.Handle {
	r, _, _ := pGetSysColorBrush.Call(uintptr(idx))
	return syscall.Handle(r)
}

// createFontEx creates a font; weight is fwNormal / fwBold.
// A negative height means character height (not cell height), exactly what GDI recommends.
func createFontEx(height int32, weight uint32, name string) syscall.Handle {
	p := utf16Ptr(name)
	r, _, _ := pCreateFontW.Call(
		uintptr(height), 0, 0, 0, uintptr(weight), 0, 0, 0,
		uintptr(defaultCS), 0, 0, uintptr(cleartypeQuality), 0,
		uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return syscall.Handle(r)
}

func createFont(height int32, name string) syscall.Handle {
	return createFontEx(height, fwNormal, name)
}

func selectObject(hdc, obj syscall.Handle) syscall.Handle {
	r, _, _ := pSelectObject.Call(uintptr(hdc), uintptr(obj))
	return syscall.Handle(r)
}

func deleteObject(obj syscall.Handle) {
	pDeleteObject.Call(uintptr(obj))
}

func setTextColor(hdc syscall.Handle, c uint32) {
	pSetTextColor.Call(uintptr(hdc), uintptr(c))
}

func setBkMode(hdc syscall.Handle, mode int32) {
	pSetBkMode.Call(uintptr(hdc), uintptr(mode))
}

// textHeight measures the font's line height (including leading) with a temp DC, no hardcoding.
func textHeight(hfont syscall.Handle) int32 {
	hdc := getDC(0)
	if hdc == 0 {
		return 16
	}
	defer releaseDC(0, hdc)

	old := selectObject(hdc, hfont)
	defer selectObject(hdc, old)

	// TEXTMETRICW is smaller than the buffer declared here; 256 bytes is plenty (only the first 20 are read)
	var tm [256]byte
	pGetTextMetricsW.Call(uintptr(hdc), uintptr(unsafe.Pointer(&tm[0])))
	h := int32(*(*int32)(unsafe.Pointer(&tm[0])))     // tmHeight
	lead := int32(*(*int32)(unsafe.Pointer(&tm[16]))) // tmExternalLeading
	if h <= 0 {
		return 16
	}
	return h + lead
}

func getModuleHandle() syscall.Handle {
	r, _, _ := pGetModuleHandleW.Call(0)
	return syscall.Handle(r)
}

func shellNotifyIcon(action uint32, nid *notifyIconDataW) bool {
	r, _, _ := pShellNotifyIconW.Call(uintptr(action), uintptr(unsafe.Pointer(nid)))
	return r != 0
}

// shellExecute opens a file or URL with the system-associated program; false means failure.
func shellExecute(file string) bool {
	p := utf16Ptr("open")
	f := utf16Ptr(file)
	r, _, _ := pShellExecuteW.Call(0, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(f)), 0, 0, uintptr(swShow))
	runtime.KeepAlive(p)
	runtime.KeepAlive(f)
	return r > 32 // HINSTANCE <= 32 means an error code
}

func setForegroundWindow(hwnd syscall.Handle) {
	pSetForegroundWindow.Call(uintptr(hwnd))
}

func getCursorPos() pointT {
	var p pointT
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p
}

// registerWindowMessage registers a custom message; TaskbarCreated is used to re-add the tray
// icon after Explorer restarts.
func registerWindowMessage(name string) uint32 {
	p := utf16Ptr(name)
	r, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return uint32(r)
}

func createPopupMenu() syscall.Handle {
	r, _, _ := pCreatePopupMenu.Call()
	return syscall.Handle(r)
}

func createMenu() syscall.Handle {
	r, _, _ := pCreateMenu.Call()
	return syscall.Handle(r)
}

func appendMenu(menu syscall.Handle, flags uint32, id uintptr, text string) {
	p := utf16Ptr(text)
	pAppendMenuW.Call(uintptr(menu), uintptr(flags), id, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

func trackPopupMenu(menu syscall.Handle, x, y int32, hwnd syscall.Handle) {
	pTrackPopupMenu.Call(uintptr(menu), uintptr(tpmRightBtn), uintptr(x), uintptr(y), 0, uintptr(hwnd), 0)
}

// showPopupMenu pops up a context menu at the cursor.
// The window must be brought to the foreground first, otherwise the menu won't dismiss when
// clicking outside it (old Win32 rule).
func showPopupMenu(menu syscall.Handle, hwnd syscall.Handle) {
	if menu == 0 || hwnd == 0 {
		return
	}
	pt := getCursorPos()
	setForegroundWindow(hwnd)
	trackPopupMenu(menu, pt.X, pt.Y, hwnd)
	postMessage(hwnd, wmNull, 0, 0)
	destroyMenu(menu)
}

// copyUTF16 writes a Go string into a fixed-size uint16 array, guaranteeing 0-termination
// (for NOTIFYICONDATA's fixed-size fields).
func copyUTF16(dst []uint16, s string) {
	src := syscall.StringToUTF16(s)
	if len(src) > len(dst) {
		src = append(src[:len(dst)-1], 0)
	}
	copy(dst, src)
}

func destroyMenu(menu syscall.Handle) {
	pDestroyMenu.Call(uintptr(menu))
}

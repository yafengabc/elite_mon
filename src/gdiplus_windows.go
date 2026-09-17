//go:build windows && gui && !tk

package main

import (
	"fmt"
	"math"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// GDI+ (gdiplus.dll) flat-API bindings, used only by the Win32 trend chart.
//
// The rest of the GUI still draws with plain GDI; only the trend chart switched
// to GDI+ so its curves and text come out anti-aliased like the web panel, without
// pulling in a new toolkit. CGO stays off — these are plain syscall bindings.
//
// GDI+ handles are opaque pointers we never dereference; they travel as uintptr in
// the Call arguments. A GpStatus of 0 means Ok.

var (
	gdip = syscall.NewLazyDLL("gdiplus.dll")

	pGdipStartup                       = gdip.NewProc("GdiplusStartup")
	pGdipShutdown                      = gdip.NewProc("GdiplusShutdown")
	pGdipCreateFromHDC                 = gdip.NewProc("GdipCreateFromHDC")
	pGdipDeleteGraphics                = gdip.NewProc("GdipDeleteGraphics")
	pGdipSetSmoothingMode              = gdip.NewProc("GdipSetSmoothingMode")
	pGdipSetTextRenderingHint          = gdip.NewProc("GdipSetTextRenderingHint")
	pGdipCreatePen1                    = gdip.NewProc("GdipCreatePen1")
	pGdipDeletePen                     = gdip.NewProc("GdipDeletePen")
	pGdipDrawLineI                     = gdip.NewProc("GdipDrawLineI")
	pGdipCreateSolidBrush              = gdip.NewProc("GdipCreateSolidFill")
	pGdipDeleteBrush                   = gdip.NewProc("GdipDeleteBrush")
	pGdipFillRectangleI                = gdip.NewProc("GdipFillRectangleI")
	pGdipDrawRectangleI                = gdip.NewProc("GdipDrawRectangleI")
	pGdipCreateFontFamilyFromName      = gdip.NewProc("GdipCreateFontFamilyFromName")
	pGdipGetGenericFontFamilySansSerif = gdip.NewProc("GdipGetGenericFontFamilySansSerif")
	pGdipDeleteFontFamily              = gdip.NewProc("GdipDeleteFontFamily")
	pGdipCreateFont                    = gdip.NewProc("GdipCreateFont")
	pGdipDeleteFont                    = gdip.NewProc("GdipDeleteFont")
	pGdipCreateStringFormat            = gdip.NewProc("GdipCreateStringFormat")
	pGdipSetStringFormatAlign          = gdip.NewProc("GdipSetStringFormatAlign")
	pGdipSetStringFormatLineAlign      = gdip.NewProc("GdipSetStringFormatLineAlign")
	pGdipDeleteStringFormat            = gdip.NewProc("GdipDeleteStringFormat")
	pGdipDrawString                    = gdip.NewProc("GdipDrawString")
	pGdipMeasureString                 = gdip.NewProc("GdipMeasureString")
)

const (
	gpUnitPixel       = 2
	gpSmoothAntiAlias = 4
	gpTextClearType   = 5
	gpAlignNear       = 0
	gpAlignCenter     = 1
	gpFontRegular     = 0
)

type gpGraphics uintptr
type gpPen uintptr
type gpBrush uintptr
type gpFont uintptr
type gpFamily uintptr
type gpFormat uintptr

type gpRectF struct{ X, Y, W, H float32 }

var (
	gdiplusToken  uintptr
	gpFamilyCache gpFamily // the one CJK-capable family all chart fonts derive from
	gpFmtNN       gpFormat // left + top
	gpFmtCC       gpFormat // centre + centre
	gpFmtNC       gpFormat // left + centre
)

type gpStartupInput struct {
	Version                uint32
	DebugEventCallback     uintptr
	SuppressBGThread       uint32
	SuppressExternalCodecs uint32
}

// gdiplusStartup initialises the GDI+ flat layer once. The font family is CJK
// capable ("Microsoft YaHei") so the Chinese axis / legend text renders; where it
// is absent (e.g. an English OS) we fall back to the generic sans-serif, which
// still draws Latin glyphs.
func gdiplusStartup() error {
	inp := gpStartupInput{Version: 1}
	r, _, _ := pGdipStartup.Call(
		uintptr(unsafe.Pointer(&gdiplusToken)),
		uintptr(unsafe.Pointer(&inp)), 0)
	if int32(r) != 0 {
		return fmt.Errorf("GdiplusStartup status %d", int32(r))
	}
	if fam, ok := gpFamilyFromName("Microsoft YaHei"); ok {
		gpFamilyCache = fam
	} else {
		gpFamilyCache, _ = gpGenericSansSerif()
	}
	gpFmtNN = gpNewFormat(gpAlignNear, gpAlignNear)
	gpFmtCC = gpNewFormat(gpAlignCenter, gpAlignCenter)
	gpFmtNC = gpNewFormat(gpAlignNear, gpAlignCenter)
	return nil
}

func gdiplusShutdown() {
	if gpFmtNN != 0 {
		pGdipDeleteStringFormat.Call(uintptr(gpFmtNN))
		pGdipDeleteStringFormat.Call(uintptr(gpFmtCC))
		pGdipDeleteStringFormat.Call(uintptr(gpFmtNC))
		gpFmtNN, gpFmtCC, gpFmtNC = 0, 0, 0
	}
	if gpFamilyCache != 0 {
		pGdipDeleteFontFamily.Call(uintptr(gpFamilyCache))
		gpFamilyCache = 0
	}
	if gdiplusToken != 0 {
		pGdipShutdown.Call(gdiplusToken)
		gdiplusToken = 0
	}
}

func gpFamilyFromName(name string) (gpFamily, bool) {
	w := utf16.Encode([]rune(name))
	w = append(w, 0)
	var f uintptr
	r, _, _ := pGdipCreateFontFamilyFromName.Call(
		uintptr(unsafe.Pointer(&w[0])), 0, uintptr(unsafe.Pointer(&f)))
	if int32(r) != 0 || f == 0 {
		return 0, false
	}
	return gpFamily(f), true
}

func gpGenericSansSerif() (gpFamily, bool) {
	var f uintptr
	r, _, _ := pGdipGetGenericFontFamilySansSerif.Call(uintptr(unsafe.Pointer(&f)))
	if int32(r) != 0 || f == 0 {
		return 0, false
	}
	return gpFamily(f), true
}

func gpNewFormat(align, line int32) gpFormat {
	var f uintptr
	pGdipCreateStringFormat.Call(0, 0, uintptr(unsafe.Pointer(&f)))
	pGdipSetStringFormatAlign.Call(uintptr(f), uintptr(align))
	pGdipSetStringFormatLineAlign.Call(uintptr(f), uintptr(line))
	return gpFormat(f)
}

func gpNewFont(px float32) gpFont {
	var f uintptr
	pGdipCreateFont.Call(
		uintptr(gpFamilyCache),
		uintptr(math.Float32bits(px)),
		uintptr(gpFontRegular),
		uintptr(gpUnitPixel),
		uintptr(unsafe.Pointer(&f)))
	return gpFont(f)
}

func gpDeleteFont(f gpFont) {
	if f != 0 {
		pGdipDeleteFont.Call(uintptr(f))
	}
}

// argb turns a GDI COLORREF (0x00BBGGRR) into a GDI+ colour (0xAARRGGBB).
func argb(cr uint32) uint32 {
	return 0xFF000000 |
		((cr & 0xFF) << 16) |
		(cr & 0xFF00) |
		((cr & 0xFF0000) >> 16)
}

func gpNewPen(color uint32, w float32) gpPen {
	var p uintptr
	pGdipCreatePen1.Call(
		uintptr(color),
		uintptr(math.Float32bits(w)),
		uintptr(gpUnitPixel),
		uintptr(unsafe.Pointer(&p)))
	return gpPen(p)
}

func gpDeletePen(p gpPen) {
	if p != 0 {
		pGdipDeletePen.Call(uintptr(p))
	}
}

func gpNewBrush(color uint32) gpBrush {
	var b uintptr
	pGdipCreateSolidBrush.Call(uintptr(color), uintptr(unsafe.Pointer(&b)))
	return gpBrush(b)
}

func gpDeleteBrush(b gpBrush) {
	if b != 0 {
		pGdipDeleteBrush.Call(uintptr(b))
	}
}

// gpCanvas wraps the GDI+ graphics object the chart draws into. It normally draws to an
// offscreen memory bitmap that is blitted to the visible HDC in a single operation on
// Close — that one BitBlt is what kills the hover flicker (the Tk build does exactly the
// same: draw to a pixmap, then Blit). If the memory DC / bitmap can't be allocated the
// canvas falls back to drawing straight to the HDC and reports offscreen=false, so the
// caller keeps drawing at the control rectangle's origin and Close becomes a no-op blit.
type gpCanvas struct {
	g         gpGraphics
	offscreen bool           // true: composed offscreen, blitted on Close
	tgt       syscall.Handle // real HDC to blit onto (offscreen only)
	dx, dy    int32          // destination origin in the target (offscreen only)
	mem       syscall.Handle // memory DC (offscreen only)
	bmp       syscall.Handle // compatible bitmap selected into mem (offscreen only)
	old       syscall.Handle // previous bitmap in mem, restored before delete (offscreen only)
	w, h      int32          // offscreen bitmap size
}

// gpFromHDC builds a canvas for the trend chart. w/h/dx/dy describe the item rectangle in
// the target HDC's coordinate space: an offscreen bitmap of (w,h) is allocated so the chart
// is composed away from the screen, then blitted at (dx,dy). On any offscreen failure it
// draws directly to hdc and reports offscreen=false so the caller's origin stays put.
func gpFromHDC(hdc syscall.Handle, w, h, dx, dy int32) (*gpCanvas, bool) {
	if w > 0 && h > 0 {
		if mem := createCompatibleDC(hdc); mem != 0 {
			if bmp := createCompatibleBitmap(hdc, w, h); bmp != 0 {
				old := selectObject(mem, bmp)
				var g uintptr
				r, _, _ := pGdipCreateFromHDC.Call(uintptr(mem), uintptr(unsafe.Pointer(&g)))
				if int32(r) == 0 && g != 0 {
					c := &gpCanvas{
						g: gpGraphics(g), offscreen: true,
						tgt: hdc, dx: dx, dy: dy,
						mem: mem, bmp: bmp, old: old, w: w, h: h,
					}
					c.g.setSmoothing()
					c.g.setTextHint()
					return c, true
				}
				// GDI+ refused the memory DC; tear the offscreen down and fall through.
				if old != 0 {
					selectObject(mem, old)
				}
				deleteObject(bmp)
				deleteDC(mem)
			}
		}
	}
	// Direct-to-screen fallback, identical to the old behaviour.
	var g uintptr
	r, _, _ := pGdipCreateFromHDC.Call(uintptr(hdc), uintptr(unsafe.Pointer(&g)))
	if int32(r) != 0 || g == 0 {
		return nil, false
	}
	c := &gpCanvas{g: gpGraphics(g), offscreen: false}
	c.g.setSmoothing()
	c.g.setTextHint()
	return c, true
}

func (c *gpCanvas) Close() {
	if c.g != 0 {
		pGdipDeleteGraphics.Call(uintptr(c.g))
		c.g = 0
	}
	if c.offscreen && c.mem != 0 {
		bitBlt(c.tgt, c.mem, c.dx, c.dy, c.w, c.h, 0, 0, srccopy)
		if c.old != 0 {
			selectObject(c.mem, c.old)
		}
		deleteObject(c.bmp)
		deleteDC(c.mem)
		c.mem, c.bmp, c.old = 0, 0, 0
	}
}

// Forwarding wrappers so the caller works against *gpCanvas, not the raw graphics
// handle. poly already lives here; these keep every draw call on the same receiver.
func (c *gpCanvas) fillRect(x, y, w, h int32, b gpBrush) { c.g.fillRect(x, y, w, h, b) }
func (c *gpCanvas) drawRect(x, y, w, h int32, pen gpPen) { c.g.drawRect(x, y, w, h, pen) }
func (c *gpCanvas) line(x1, y1, x2, y2 int32, pen gpPen) { c.g.line(x1, y1, x2, y2, pen) }
func (c *gpCanvas) text(s string, x, y, w, h float32, fmt gpFormat, font gpFont, b gpBrush) {
	c.g.text(s, x, y, w, h, fmt, font, b)
}

func (g gpGraphics) setSmoothing() {
	pGdipSetSmoothingMode.Call(uintptr(g), gpSmoothAntiAlias)
}

func (g gpGraphics) setTextHint() {
	pGdipSetTextRenderingHint.Call(uintptr(g), gpTextClearType)
}

func (g gpGraphics) line(x1, y1, x2, y2 int32, pen gpPen) {
	pGdipDrawLineI.Call(uintptr(g), uintptr(pen),
		uintptr(x1), uintptr(y1), uintptr(x2), uintptr(y2))
}

func (g gpGraphics) fillRect(x, y, w, h int32, b gpBrush) {
	pGdipFillRectangleI.Call(uintptr(g), uintptr(b),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h))
}

func (g gpGraphics) drawRect(x, y, w, h int32, pen gpPen) {
	pGdipDrawRectangleI.Call(uintptr(g), uintptr(pen),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h))
}

// poly strokes a chain of laid-out points as one connected line.
func (c *gpCanvas) poly(pts []trendXY, ox, oy int32, pen gpPen) {
	for i := 1; i < len(pts); i++ {
		c.g.line(ox+pts[i-1].X, oy+pts[i-1].Y, ox+pts[i].X, oy+pts[i].Y, pen)
	}
}

func (g gpGraphics) text(s string, x, y, w, h float32, fmt gpFormat, font gpFont, b gpBrush) {
	wr := utf16.Encode([]rune(s))
	wr = append(wr, 0)
	rf := gpRectF{X: x, Y: y, W: w, H: h}
	pGdipDrawString.Call(
		uintptr(g),
		uintptr(unsafe.Pointer(&wr[0])),
		uintptr(len(wr)-1),
		uintptr(font),
		uintptr(unsafe.Pointer(&rf)),
		uintptr(fmt),
		uintptr(b))
}

// measure returns GDI+'s own layout box for s drawn with the given font, wrapped at
// wrapW (pass a huge width for a single-line measurement). Everything the chart
// positions by text size must measure through here: mixing in a GDI measurement
// made the legend overlap and the hover box clip its second line.
func (c *gpCanvas) measure(s string, font gpFont, wrapW float32) gpRectF {
	wr := utf16.Encode([]rune(s))
	wr = append(wr, 0)
	rf := gpRectF{0, 0, wrapW, 10000}
	var out gpRectF
	pGdipMeasureString.Call(
		uintptr(c.g),
		uintptr(unsafe.Pointer(&wr[0])),
		uintptr(len(wr)-1),
		uintptr(font),
		uintptr(unsafe.Pointer(&rf)),
		uintptr(gpFmtNN),
		uintptr(unsafe.Pointer(&out)), 0, 0)
	return out
}

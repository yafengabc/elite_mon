//go:build gui

package main

import (
	"math"
	"strconv"
)

// Kill-trend geometry shared by the two GUI backends.
//
// The web panel draws this chart as hand-written SVG (static/main.js); the desktop
// builds cannot reuse that code, so the layout lives here in platform-neutral form
// and both backends only rasterise what they are handed. That keeps the Win32 and
// Tk charts identical instead of drifting apart, and it makes the geometry unit
// testable without a window.
//
// Everything is in pixels: the caller supplies the drawing area's size.

const (
	trendPadL = 10 // breathing space before the oldest point
	trendPadR = 54 // room for the tick column, sized for the English unit "kills/h"
	trendPadT = 30 // unit caption above the plot, then some air
	trendPadB = 22 // the time labels along the bottom

	trendTickRows = 4 // grid rows; overriding this changes nothing else
	trendMinAxis  = 4 // an all-flat chart still needs a non-zero top to divide
	trendLabels   = 8 // time labels are thinned to about this many

	// trendLabelHalfW caps how much room a centred clock label may claim. The
	// outermost labels are pulled inside by this much, otherwise the first and last
	// ones ("18:50", "00:40") hang over the edge and get clipped to "8:50".
	// It is a budget rather than a measurement: the two toolkits measure text
	// differently, and a label wider than its neighbour is worse than a short one.
	trendLabelHalfW = 26

	trendMinW = 120 // below this the plot area has no room and only the note is drawn
	trendMinH = 80

	// trendTipW is the wrap width of the hover readout. It lives here because it
	// is part of how the readout is laid out, not of how it is painted: a wider
	// box breaks the line in the wrong place ("近 1" / "小时 57 杀"), and the two
	// backends drifting apart is exactly what this file exists to prevent.
	// The web panel has no wrap at all — its readout is the browser's own tooltip.
	trendTipW = 240
)

// trendChart is one laid-out frame: everything a backend needs to rasterise it.
type trendChart struct {
	Empty bool   // nothing to plot yet; show Note instead of drawing
	Note  string // "no trend data yet" / "no kills in the window"

	AxisX int32 // x of the tick column, drawn on the right like the web panel
	Max   float64

	// Plot band, for the hover guide line and for rejecting a cursor that is
	// outside the chart.
	PlotTop, PlotBottom int32

	GridY  []int32      // y of each grid line, top row first
	Ticks  []trendTick  // the labels beside them, kills per hour
	Labels []trendLabel // thinned clock times along the bottom
	Rate   []trendXY    // green: this cell's kill count as an hourly rate
	Hour   []trendXY    // blue: rolling one-hour kill count

	// Cols carries the same buckets with their numbers, so a hover readout needs
	// nothing but the chart it was laid out from.
	Cols []trendCol

	LegendRate string // 击杀速率（近 10 分钟折算）
	LegendHour string // 最近 1 小时击杀
	Span       string // 全程 3 小时 20 分
	Unit       string // 杀/时
}

type trendTick struct {
	Y    int32
	Text string
}

// trendLabel is centred on X; Y is the top of its text box. Centring is the
// caller's business because the two toolkits measure text differently.
type trendLabel struct {
	X, Y int32
	Text string
}

type trendXY struct{ X, Y int32 }

// trendCol is one bucket as plotted: where its column sits and the numbers behind
// it. Keeping them means a hover readout is a lookup rather than a second pass over
// the monitor's data.
type trendCol struct {
	X         int32
	TimeLocal string
	Rate      float64 // kills per hour, i.e. Kills × trendCellsPerHour
	Kills     int
	KillsHour int
	Bounty    int64
}

// step is the horizontal pitch between columns (0 when there is only one).
func (c trendChart) step() int32 {
	if len(c.Cols) < 2 {
		return 0
	}
	return c.Cols[1].X - c.Cols[0].X
}

// ColumnAt returns the bucket the cursor is over, or -1 when it is outside the
// plot. Nearest column rather than x/step, so the readout stays truthful at the
// ends and with a single column.
func (c trendChart) ColumnAt(x, y int32) int {
	if c.Empty || len(c.Cols) == 0 {
		return -1
	}
	// A little slack below the plot: the time labels are clickable-looking and
	// hovering them should still name the column.
	if y < c.PlotTop-6 || y > c.PlotBottom+18 {
		return -1
	}
	step := c.step()
	if step == 0 {
		step = 16 // one column: treat it as its own narrow band
	}
	if x < c.Cols[0].X-step/2 || x > c.Cols[len(c.Cols)-1].X+step/2 {
		return -1
	}
	best, bestD := 0, abs32(x-c.Cols[0].X)
	for i := 1; i < len(c.Cols); i++ {
		if d := abs32(x - c.Cols[i].X); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// HoverTip is the readout for one bucket. It uses the very template the web panel
// puts in its <title>, so the two read the same, and the panel's tooltip wording
// drives both.
func (c trendChart) HoverTip(i int) string {
	if i < 0 || i >= len(c.Cols) {
		return ""
	}
	col := c.Cols[i]
	return T("panel.tip", col.TimeLocal,
		commas(int64(math.Round(col.Rate))), col.Kills, col.KillsHour, commas(col.Bounty))
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// trendKey is everything that changes what a chart draws: the point count (a new
// bucket appeared), the newest point (the usual case — a kill landing in it) and
// the covered span (a new session resets the lot).
//
// Comparing the struct is enough, so neither backend needs to stringify anything
// to decide whether a repaint is due — and both share one definition of "changed".
type trendKey struct {
	n    int
	span string
	last TrendPoint
}

func trendSignature(st AppStatus) trendKey {
	k := trendKey{n: len(st.KillTrend), span: st.TrendWindowText}
	if k.n > 0 {
		k.last = st.KillTrend[k.n-1]
	}
	return k
}

// buildTrendChart lays the points out in a w×h area. pts is what the monitor
// publishes (one trendBucket cell each), winText is the human-readable span it
// covers ("3 小时 20 分") and may be empty.
func buildTrendChart(pts []TrendPoint, winText string, w, h int32) trendChart {
	c := trendChart{
		LegendRate: T("panel.legend_10m"),
		LegendHour: T("panel.legend_1h"),
		Unit:       T("panel.axis_rate"),
	}
	if len(pts) == 0 {
		c.Empty, c.Note = true, T("panel.no_trend")
		return c
	}
	span := winText
	if span == "" {
		span = T("panel.session")
	}
	c.Span = T("panel.trend_all", span)

	// One shared kills-per-hour axis: the rolling hour count already is one, and
	// a cell's own count is extrapolated onto it. Both curves then live on the
	// same scale, so the higher line really is the busier stretch.
	rate := make([]float64, len(pts))
	hour := make([]float64, len(pts))
	maxV := 0.0
	for i, p := range pts {
		r := float64(p.Kills) * float64(trendCellsPerHour)
		k := float64(p.KillsHour)
		rate[i], hour[i] = r, k
		if r > maxV {
			maxV = r
		}
		if k > maxV {
			maxV = k
		}
	}
	if maxV == 0 {
		c.Empty, c.Note = true, T("panel.trend_none", span)
		return c
	}
	// A quartered axis needs a top that divides by four, so every tick label
	// stays a whole number.
	c.Max = math.Max(trendMinAxis, math.Ceil(maxV/trendTickRows)*trendTickRows)

	plotL, plotT := int32(trendPadL), int32(trendPadT)
	plotR, plotB := w-int32(trendPadR), h-int32(trendPadB)
	if plotR-plotL < trendMinW || plotB-plotT < trendMinH {
		c.Empty = true // too small for a chart; the caller draws the note instead
		return c
	}
	iw, ih := plotR-plotL, plotB-plotT
	c.AxisX = plotR
	c.PlotTop, c.PlotBottom = plotT, plotB

	n := len(pts)
	step := 0.0
	if n > 1 {
		step = float64(iw) / float64(n-1)
	}
	xOf := func(i int) int32 {
		if n == 1 {
			return plotL + iw/2
		}
		return plotL + int32(math.Round(step*float64(i)))
	}
	yOf := func(v float64) int32 {
		return plotT + ih - int32(math.Round(float64(ih)*(v/c.Max)))
	}

	for i := 0; i <= trendTickRows; i++ {
		v := c.Max * float64(trendTickRows-i) / float64(trendTickRows)
		y := yOf(v)
		c.GridY = append(c.GridY, y)
		c.Ticks = append(c.Ticks, trendTick{Y: y, Text: strconv.Itoa(int(math.Round(v)))})
	}

	every := (n + trendLabels - 1) / trendLabels
	if every < 1 {
		every = 1
	}
	for i := 0; i < n; i += every {
		// Pull the outermost labels inside the canvas: they are drawn centred, so
		// one at x=8 would lose its first character.
		x := xOf(i)
		if x < trendLabelHalfW {
			x = trendLabelHalfW
		}
		if x > w-trendLabelHalfW {
			x = w - trendLabelHalfW
		}
		c.Labels = append(c.Labels, trendLabel{X: x, Y: plotB + 4, Text: pts[i].TimeLocal})
	}

	c.Rate = make([]trendXY, n)
	c.Hour = make([]trendXY, n)
	c.Cols = make([]trendCol, n)
	for i, p := range pts {
		c.Rate[i] = trendXY{X: xOf(i), Y: yOf(rate[i])}
		c.Hour[i] = trendXY{X: xOf(i), Y: yOf(hour[i])}
		c.Cols[i] = trendCol{
			X: xOf(i), TimeLocal: p.TimeLocal,
			Rate: rate[i], Kills: p.Kills, KillsHour: p.KillsHour, Bounty: p.Bounty,
		}
	}
	return c
}

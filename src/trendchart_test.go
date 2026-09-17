//go:build gui

package main

import (
	"math"
	"reflect"
	"strconv"
	"testing"
)

func TestBuildTrendChartEmptyStates(t *testing.T) {
	// No points at all is "nothing to show yet", distinct from "the whole window has no kills".
	if got := buildTrendChart(nil, "", 600, 300); !got.Empty || got.Note != T("panel.no_trend") {
		t.Errorf("no points: Empty=%v Note=%q, want Empty=true Note=%q", got.Empty, got.Note, T("panel.no_trend"))
	}

	// Points exist but nobody has been killed: the span is named in the message.
	flat := []TrendPoint{{TimeLocal: "10:00"}, {TimeLocal: "10:10"}, {TimeLocal: "10:20"}}
	got := buildTrendChart(flat, "1 小时", 600, 300)
	if !got.Empty || got.Note != T("panel.trend_none", "1 小时") {
		t.Errorf("no kills: Empty=%v Note=%q", got.Empty, got.Note)
	}
	// A missing window text falls back to "本局" rather than printing an empty gap.
	got = buildTrendChart(flat, "", 600, 300)
	if got.Note != T("panel.trend_none", T("panel.session")) {
		t.Errorf("no window text: Note=%q", got.Note)
	}
}

// A tiny drawing area must say so instead of drawing lines through each other.
func TestBuildTrendChartTooSmall(t *testing.T) {
	pts := []TrendPoint{{TimeLocal: "10:00", Kills: 2}, {TimeLocal: "10:10", Kills: 3}}
	if got := buildTrendChart(pts, "", 60, 40); !got.Empty {
		t.Error("too small: want Empty")
	}
	if got := buildTrendChart(pts, "", 600, 300); got.Empty {
		t.Error("enough room: want a chart")
	}
}

func TestBuildTrendChartScalesCellCountsToRate(t *testing.T) {
	// One cell's own count is a 10-minute bucket; the chart must extrapolate it
	// by trendCellsPerHour so it shares the hour curve's scale.
	pts := []TrendPoint{
		{TimeLocal: "10:00", Kills: 1, KillsHour: 2},
		{TimeLocal: "10:10", Kills: 3, KillsHour: 4},
		{TimeLocal: "10:20", Kills: 0, KillsHour: 4},
	}
	c := buildTrendChart(pts, "20 分钟", 640, 240)

	if c.Empty {
		t.Fatal("want a chart")
	}
	// max(1*6, 2, 3*6, 4, 0, 4) = 18 -> next multiple of 4 = 20
	if want := float64(trendMinAxis); c.Max < want {
		t.Errorf("Max = %v, must never be below %v", c.Max, want)
	}
	if c.Max != 20 {
		t.Errorf("Max = %v, want 20 (ceil(18/4)*4)", c.Max)
	}
	if len(c.Rate) != len(pts) || len(c.Hour) != len(pts) {
		t.Fatalf("point counts: rate=%d hour=%d, want %d", len(c.Rate), len(c.Hour), len(pts))
	}

	// The peak of the rate curve (3*6=18 of 20) must sit above the flat hour curve (4 of 20).
	top := func(pts []trendXY) (min int32) {
		min = pts[0].Y
		for _, p := range pts {
			if p.Y < min {
				min = p.Y
			}
		}
		return min
	}
	if top(c.Rate) >= top(c.Hour) {
		t.Errorf("rate peak y=%d should be above the hour peak y=%d (smaller y = higher)",
			top(c.Rate), top(c.Hour))
	}

	// X must advance left to right, and the two series must share it exactly.
	for i := 1; i < len(pts); i++ {
		if c.Rate[i].X <= c.Rate[i-1].X {
			t.Errorf("x not increasing at %d: %d -> %d", i, c.Rate[i-1].X, c.Rate[i].X)
		}
	}
	for i := range pts {
		if c.Rate[i].X != c.Hour[i].X {
			t.Errorf("point %d: series disagree on x (%d vs %d)", i, c.Rate[i].X, c.Hour[i].X)
		}
	}

	// Everything drawn must stay inside the given box.
	for _, p := range append(append([]trendXY{}, c.Rate...), c.Hour...) {
		if p.X < 0 || p.X > 640 || p.Y < 0 || p.Y > 240 {
			t.Errorf("point (%d,%d) escapes the 640x240 area", p.X, p.Y)
		}
	}
}

func TestBuildTrendChartAxisAndLabels(t *testing.T) {
	pts := make([]TrendPoint, 100) // far more points than there are tick rows
	for i := range pts {
		pts[i] = TrendPoint{TimeLocal: "10:00", Kills: i % 5, KillsHour: i % 7}
	}
	c := buildTrendChart(pts, "", 640, 240)

	if len(c.GridY) != trendTickRows+1 || len(c.Ticks) != trendTickRows+1 {
		t.Fatalf("grid rows = %d/%d, want %d", len(c.GridY), len(c.Ticks), trendTickRows+1)
	}
	// Top row carries Max, bottom row zero, and they are ordered downward.
	if c.Ticks[0].Text != strconv.Itoa(int(math.Round(c.Max))) {
		t.Errorf("top tick = %q with Max=%v", c.Ticks[0].Text, c.Max)
	}
	if c.Ticks[len(c.Ticks)-1].Text != "0" {
		t.Errorf("bottom tick = %q, want 0", c.Ticks[len(c.Ticks)-1].Text)
	}
	for i := 1; i < len(c.Ticks); i++ {
		if c.Ticks[i].Y <= c.Ticks[i-1].Y {
			t.Errorf("tick %d is not below the previous one", i)
		}
	}
	if c.AxisX >= 640-int32(trendPadR)/2 {
		t.Errorf("AxisX=%d leaves no room for the tick column", c.AxisX)
	}

	// Labels must be thinned, otherwise 100 of them overlap into a blur.
	if len(c.Labels) > trendLabels+1 {
		t.Errorf("%d time labels, want at most %d", len(c.Labels), trendLabels+1)
	}
	if len(c.Labels) == 0 {
		t.Error("no time labels at all")
	}
}

// A single point has no step to interpolate, but it still has to land somewhere
// sensible instead of dividing by zero (the web panel centres it too).
func TestBuildTrendChartSinglePoint(t *testing.T) {
	pts := []TrendPoint{{TimeLocal: "10:00", Kills: 4, KillsHour: 4}}
	c := buildTrendChart(pts, "", 640, 240)
	if c.Empty {
		t.Fatal("single point should still be drawn")
	}
	x := c.Rate[0].X
	if x <= 0 || x >= 640 {
		t.Errorf("single x = %d, want inside the area", x)
	}
	// Axis top comes from whichever series is higher: here it is the rate
	// (4 kills in a cell -> 4*trendCellsPerHour = 24/h), so the rate point sits on
	// the top grid row and the hour curve (4/h) low down - which is exactly why
	// both share one axis.
	if c.Max != 4*float64(trendCellsPerHour) {
		t.Errorf("Max = %v, want %v", c.Max, 4*float64(trendCellsPerHour))
	}
	topRow, bottomRow := c.GridY[0], c.GridY[len(c.GridY)-1]
	if c.Rate[0].Y != topRow {
		t.Errorf("rate y=%d, want the top grid row %d", c.Rate[0].Y, topRow)
	}
	if c.Hour[0].Y <= (topRow+bottomRow)/2 {
		t.Errorf("hour y=%d should sit low, the hour count is well under Max", c.Hour[0].Y)
	}
}

func TestBuildTrendChartFieldsAreResetPerCall(t *testing.T) {
	// Two calls with different data must not accumulate: the same value is reused
	// and every slice is rebuilt.
	a := buildTrendChart([]TrendPoint{{Kills: 1, TimeLocal: "10:00"}, {Kills: 2, TimeLocal: "10:10"}}, "", 640, 240)
	b := buildTrendChart([]TrendPoint{{Kills: 1, TimeLocal: "11:00"}}, "", 640, 240)
	if len(b.Rate) != 1 || len(b.Hour) != 1 {
		t.Errorf("second call kept the earlier point count: %d/%d", len(b.Rate), len(b.Hour))
	}
	if reflect.DeepEqual(a.Rate, b.Rate) {
		t.Error("second call reused the previous points")
	}
}

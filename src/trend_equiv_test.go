package main

import (
	"math/rand"
	"runtime"
	"sort"
	"testing"
	"time"
)

// allocBytesPerRun returns the average heap bytes allocated per call to f.
//
// Better suited than testing.AllocsPerRun for asserting "allocation is independent of
// backlog":
//   - a whole-table copy adds only 1 alloc, so comparing counts degenerates into "the
//     two sides must be strictly equal";
//   - under -race, AllocsPerRun is disturbed by the race runtime's shadow memory:
//     measured 63/64 with ±1 jitter, so strict equality fails spuriously.
//
// The byte gap is 2KB vs 174KB (~80x), which the jitter cannot mask.
func allocBytesPerRun(runs int, f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

// Reference implementation: the pre-change version (copy non-mission kills + sort +
// two cursors). Test-only; proves the new single-cursor implementation is
// point-for-point equivalent.
func refFillWindows(m *monitor, now, start time.Time, points []TrendPoint) {
	type kill struct {
		t       time.Time
		credits int64
	}
	evts := make([]kill, 0, len(m.bounties))
	for _, b := range m.bounties {
		if !b.mission {
			evts = append(evts, kill{b.t, b.credits})
		}
	}
	sort.Slice(evts, func(i, j int) bool { return evts[i].t.Before(evts[j].t) })

	lo10, lo60 := 0, 0
	for i := range points {
		gs := start.Add(time.Duration(i) * trendBucket)
		e := gs.Add(trendBucket)
		if i == len(points)-1 && now.Before(e) && !now.Before(gs) {
			e = now
		}
		cut10, cut60 := e.Add(-trendBucket), e.Add(-trendRolling)
		for lo10 < len(evts) && evts[lo10].t.Before(cut10) {
			lo10++
		}
		for lo60 < len(evts) && evts[lo60].t.Before(cut60) {
			lo60++
		}
		var k int
		var cr int64
		for j := lo10; j < len(evts) && evts[j].t.Before(e); j++ {
			k++
			cr += evts[j].credits
		}
		hi := lo60
		for hi < len(evts) && evts[hi].t.Before(e) {
			hi++
		}
		points[i].Kills, points[i].Bounty, points[i].KillsHour = k, cr, hi-lo60
	}
}

func refKillTrend(m *monitor, now time.Time) ([]TrendPoint, string) {
	var first, last time.Time
	for _, b := range m.bounties {
		if b.mission {
			continue
		}
		if first.IsZero() || b.t.Before(first) {
			first = b.t
		}
		if last.IsZero() || b.t.After(last) {
			last = b.t
		}
	}
	if first.IsZero() {
		return nil, ""
	}
	start := first.Truncate(trendBucket)
	end := last.Truncate(trendBucket)
	if cur := now.Truncate(trendBucket); cur.After(end) {
		end = cur
	}
	buckets := int(end.Sub(start)/trendBucket) + 1
	if buckets > maxTrendPoints {
		start = end.Add(-time.Duration(maxTrendPoints-1) * trendBucket)
		buckets = maxTrendPoints
	}
	points := make([]TrendPoint, buckets)
	for i := range points {
		points[i].TimeLocal = fmtTime(start.Add(time.Duration(i) * trendBucket))[11:16]
	}
	refFillWindows(m, now, start, points)
	return points, spanText(end.Add(trendBucket).Sub(start))
}

func compareWithRef(t *testing.T, label string, m *monitor, now time.Time) {
	t.Helper()
	got, gspan := m.killTrend(now)
	want, wspan := refKillTrend(m, now)

	if gspan != wspan {
		t.Fatalf("%s：跨度文案不同：新 %q，旧 %q", label, gspan, wspan)
	}
	if len(got) != len(want) {
		t.Fatalf("%s：点数不同：新 %d，旧 %d", label, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s：第 %d 格（%s）不同：新 {%d, %d, %d}，旧 {%d, %d, %d}",
				label, i, want[i].TimeLocal,
				got[i].Kills, got[i].Bounty, got[i].KillsHour,
				want[i].Kills, want[i].Bounty, want[i].KillsHour)
		}
	}
}

// The single-cursor implementation must match the "copy + sort" reference point for
// point. Data is always generated in increasing time order - the real shape of
// m.bounties (appended in journal order).
func TestKillTrendMatchesReference(t *testing.T) {
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

	// 1) Deterministic pseudo-random: 20 hours, mission rewards interleaved, with gaps
	rng := rand.New(rand.NewSource(20260911))
	var mixed []bountyEvent
	cur := base
	for i := 0; i < 3000 && cur.Before(base.Add(20*time.Hour)); i++ {
		cur = cur.Add(time.Duration(rng.Intn(40)) * time.Second)
		mission := rng.Intn(7) == 0
		mixed = append(mixed, bountyEvent{
			t: cur, credits: int64(rng.Intn(500000) + 1),
			shipType: "eagle", mission: mission,
		})
	}
	compareWithRef(t, "20 小时混合", &monitor{bounties: mixed}, base.Add(20*time.Hour))

	// 2) Mission rewards at the head and tail: the end cursors must skip past them
	withMissionsAtEnds := []bountyEvent{
		{t: base, credits: 1, mission: true},
		{t: base.Add(time.Minute), credits: 1, mission: true},
		{t: base.Add(31 * time.Minute), credits: 70, shipType: "eagle"},
		{t: base.Add(time.Hour), credits: 1, mission: true},
		{t: base.Add(time.Hour + time.Minute), credits: 1, mission: true},
	}
	compareWithRef(t, "任务顶首尾", &monitor{bounties: withMissionsAtEnds}, base.Add(2*time.Hour))

	// 3) Kills landing exactly on bucket boundaries
	edges := []bountyEvent{
		{t: base.Add(10 * time.Minute), credits: 10, shipType: "eagle"},
		{t: base.Add(20 * time.Minute), credits: 20, shipType: "eagle"},
		{t: base.Add(time.Hour), credits: 60, shipType: "eagle"},
		{t: base.Add(time.Hour + 10*time.Minute), credits: 70, shipType: "eagle"},
	}
	compareWithRef(t, "格子边界", &monitor{bounties: edges}, base.Add(90*time.Minute))

	// 4) Dense small intervals: the rolling window moves at every point
	var dense []bountyEvent
	for i := 0; i < 4000; i++ {
		dense = append(dense, bountyEvent{
			t: base.Add(time.Duration(i) * 13 * time.Second), credits: 100, shipType: "eagle",
		})
	}
	compareWithRef(t, "密集 13 秒间隔", &monitor{bounties: dense}, base.Add(15*time.Hour))

	// 5) Beyond maxTrendPoints: the capping logic must match
	var long []bountyEvent
	for i := 0; i < 200; i++ {
		long = append(long, bountyEvent{
			t: base.Add(time.Duration(i) * 6 * trendBucket), credits: int64(i + 1), shipType: "eagle",
		})
	}
	compareWithRef(t, "超长封顶", &monitor{bounties: long},
		base.Add(time.Duration(200*6)*trendBucket))

	// 6) Only one kill plus many mission rewards
	single := []bountyEvent{{t: base, credits: 5, mission: true}}
	for i := 0; i < 50; i++ {
		single = append(single, bountyEvent{t: base.Add(time.Duration(i) * time.Minute), credits: 1, shipType: "eagle"})
	}
	compareWithRef(t, "首条是任务", &monitor{bounties: single}, base.Add(time.Hour))
}

// A fully reversed list must not panic (a negative bucket count blows up make).
// Real data never looks like this, but the fast path assumes ordering, so it needs a
// guard.
func TestKillTrendReversedDoesNotPanic(t *testing.T) {
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	m := &monitor{bounties: []bountyEvent{
		{t: base.Add(time.Hour), credits: 3, shipType: "eagle"},
		{t: base.Add(30 * time.Minute), credits: 2, shipType: "eagle"},
		{t: base, credits: 1, shipType: "eagle"},
	}}
	pts, span := m.killTrend(base.Add(2 * time.Hour))
	if len(pts) == 0 || span == "" {
		t.Fatalf("倒序输入不该返回空结果：%d 个点，跨度 %q", len(pts), span)
	}
}

// The fast path's core promise: per-round allocation depends only on the bucket count,
// not on how many entries are backlogged. Before the change each round copied the whole
// kill list (5000 entries = 174KB), so the allocation count exploded with the backlog.
func TestKillTrendAllocsIndependentOfBacklog(t *testing.T) {
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	span := 10 * time.Hour

	// Both ends pinned at base / base+span so both cases draw the same bucket count
	build := func(n int) *monitor {
		bs := make([]bountyEvent, 0, n)
		step := span / time.Duration(n-1)
		for i := 0; i < n; i++ {
			bs = append(bs, bountyEvent{
				t: base.Add(time.Duration(i) * step), credits: 1000, shipType: "eagle",
			})
		}
		return &monitor{bounties: bs}
	}

	now := base.Add(span)
	small, big := build(20), build(5000)

	// First confirm both draw the same bucket count, otherwise the allocation numbers are incomparable
	if p1, _ := small.killTrend(now); len(p1) == 0 {
		t.Fatal("小样本没画出点")
	}
	sp, _ := small.killTrend(now)
	bp, _ := big.killTrend(now)
	if len(sp) != len(bp) {
		t.Fatalf("两种情况格子数不同（%d vs %d），没法比较分配", len(sp), len(bp))
	}

	b1 := allocBytesPerRun(20, func() { small.killTrend(now) })
	b2 := allocBytesPerRun(20, func() { big.killTrend(now) })
	t.Logf("每轮分配：积压 20 条 → %d B | 积压 5000 条 → %d B（格子 %d 个）",
		b1, b2, len(sp))

	// After decoupling, both allocate only the TrendPoints themselves (~1.5KB, tied to the
	// bucket count). If anyone brings the whole-table copy back, b2 jumps to ~174KB, far
	// past this threshold.
	if b2 > b1*3+8192 {
		t.Errorf("分配量随积压增长：20 条 %d B/轮，5000 条 %d B/轮 —— 又在整表复制了", b1, b2)
	}
}

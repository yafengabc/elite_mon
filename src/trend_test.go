package main

import (
	"testing"
	"time"
)

// Bucketing is the trend chart's foundation: a wrong bucket origin, a missed edge
// entry, or a mission reward slipping in makes everything on the chart fake. Fixed
// timestamps pin each case down here.
func TestKillTrend(t *testing.T) {
	// base falls exactly on a 10-minute boundary, saving alignment math
	base := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)

	m := &monitor{
		bounties: []bountyEvent{
			{t: base.Add(5 * time.Minute), credits: 100, shipType: "sidewinder"}, // bucket 0
			{t: base.Add(12 * time.Minute), credits: 250, shipType: "eagle"},     // bucket 1
			{t: base.Add(35 * time.Minute), credits: 40, shipType: "hauler"},     // bucket 3
			{t: base.Add(25 * time.Minute), credits: 500000, mission: true},      // mission reward: neither line moves
		},
	}

	pts, span := m.killTrend(base)

	// Full span: first entry -> last entry, 4 buckets (bucket 2 is a gap but is kept)
	if len(pts) != 4 {
		t.Fatalf("点数不对：想要 4，得到 %d", len(pts))
	}
	if span != "40 分钟" {
		t.Errorf("跨度文案不对：想要 %q，得到 %q", "40 分钟", span)
	}
	if pts[0].Kills != 1 || pts[0].Bounty != 100 {
		t.Errorf("第 0 格应为 1 杀 / 100 Cr，实际 %d 杀 / %d Cr", pts[0].Kills, pts[0].Bounty)
	}
	if pts[1].Kills != 1 || pts[1].Bounty != 250 {
		t.Errorf("第 1 格应为 1 杀 / 250 Cr，实际 %d 杀 / %d Cr", pts[1].Kills, pts[1].Bounty)
	}
	if pts[2].Kills != 0 || pts[2].Bounty != 0 {
		t.Errorf("第 2 格是空档，应为 0，实际 %d 杀 / %d Cr", pts[2].Kills, pts[2].Bounty)
	}
	if pts[3].Kills != 1 || pts[3].Bounty != 40 {
		t.Errorf("第 3 格应为 1 杀 / 40 Cr，实际 %d 杀 / %d Cr", pts[3].Kills, pts[3].Bounty)
	}

	var kills int
	var bounty int64
	for _, p := range pts {
		kills += p.Kills
		bounty += p.Bounty
	}
	if kills != 3 || bounty != 390 {
		t.Errorf("任务奖励没排除干净：合计 %d 杀 / %d Cr（应为 3 / 390）", kills, bounty)
	}

	// Time labels come from the bucket origin, formatted HH:MM
	if len(pts[0].TimeLocal) != 5 || pts[0].TimeLocal[2] != ':' {
		t.Errorf("时间标签格式不对：%q", pts[0].TimeLocal)
	}
	if pts[0].TimeLocal != fmtTime(base)[11:16] {
		t.Errorf("第 0 格标签应为 %q，实际 %q", fmtTime(base)[11:16], pts[0].TimeLocal)
	}

	// The full span is decided by kills alone: two consecutive calls must agree (no drift with time.Now())
	again, _ := m.killTrend(base)
	if pts[0].TimeLocal != again[0].TimeLocal || len(pts) != len(again) {
		t.Errorf("两次调用结果不同：%d 点/%q vs %d 点/%q",
			len(pts), pts[0].TimeLocal, len(again), again[0].TimeLocal)
	}
}

// The first kill usually lands mid-bucket, so the origin must align backwards to the
// 10-minute boundary; otherwise the whole curve shifts on every refresh.
func TestKillTrendStartAligned(t *testing.T) {
	base := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)
	m := &monitor{
		bounties: []bountyEvent{
			{t: base.Add(37 * time.Minute), credits: 10, shipType: "eagle"},
		},
	}
	pts, span := m.killTrend(base)
	if len(pts) != 1 {
		t.Fatalf("一条击杀应只有 1 格，实际 %d", len(pts))
	}
	want := fmtTime(base.Add(30 * time.Minute))[11:16]
	if pts[0].TimeLocal != want {
		t.Errorf("起点没对齐到 10 分钟边界：想要 %q，实际 %q", want, pts[0].TimeLocal)
	}
	if pts[0].Kills != 1 {
		t.Errorf("击杀数不对：想要 1，实际 %d", pts[0].Kills)
	}
	if span != "10 分钟" {
		t.Errorf("跨度文案不对：想要 %q，实际 %q", "10 分钟", span)
	}
}

// Mission rewards only, never a single kill: no points should be drawn.
func TestKillTrendNoKills(t *testing.T) {
	base := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)
	m := &monitor{
		bounties: []bountyEvent{
			{t: base, credits: 2400000, mission: true},
		},
	}
	pts, span := m.killTrend(base)
	if pts != nil {
		t.Errorf("没击杀时应返回 nil，实际 %d 个点", len(pts))
	}
	if span != "" {
		t.Errorf("没击杀时跨度文案应为空，实际 %q", span)
	}

	// An empty monitor must not blow up either
	if p, _ := (&monitor{}).killTrend(base); p != nil {
		t.Errorf("空 monitor 应返回 nil，实际 %d 个点", len(p))
	}
}

// Guard for very long idle stretches: cap the point count, dropping the oldest and
// keeping the most recent stretch.
func TestKillTrendMaxPoints(t *testing.T) {
	base := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)
	m := &monitor{
		bounties: []bountyEvent{
			{t: base, credits: 1, shipType: "eagle"},
			{t: base.Add(time.Duration(maxTrendPoints+9) * trendBucket), credits: 2, shipType: "eagle"},
		},
	}
	pts, _ := m.killTrend(base)
	if len(pts) != maxTrendPoints {
		t.Fatalf("点数应封顶在 %d，实际 %d", maxTrendPoints, len(pts))
	}
	want := fmtTime(base.Add(10 * trendBucket))[11:16]
	if pts[0].TimeLocal != want {
		t.Errorf("封顶后应丢掉最早的 10 格，首点应为 %q，实际 %q", want, pts[0].TimeLocal)
	}
	if pts[len(pts)-1].Kills != 1 {
		t.Errorf("最后一格应保留那条击杀，实际 %d", pts[len(pts)-1].Kills)
	}
}

// The right end of the curve must track the current time: trailing zeros after the
// player stops are correct - nothing was killed in that stretch.
func TestKillTrendEndsAtNow(t *testing.T) {
	base := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)
	m := &monitor{
		bounties: []bountyEvent{
			{t: base.Add(5 * time.Minute), credits: 100, shipType: "eagle"},
		},
	}
	// now is 65 minutes after base -> lands in the base+60min bucket, so the curve should reach it
	now := base.Add(65 * time.Minute)
	pts, span := m.killTrend(now)
	if len(pts) != 7 {
		t.Fatalf("应画到第 7 格（当前时间所在格），实际 %d 格", len(pts))
	}
	want := fmtTime(base.Add(60 * time.Minute))[11:16]
	if pts[len(pts)-1].TimeLocal != want {
		t.Errorf("末点应为当前时间所在格 %q，实际 %q", want, pts[len(pts)-1].TimeLocal)
	}
	if pts[0].Kills != 1 || pts[len(pts)-1].Kills != 0 {
		t.Errorf("击杀分布不对：首格 %d，末格 %d（应 1 / 0）", pts[0].Kills, pts[len(pts)-1].Kills)
	}
	if span != "1 小时 10 分" {
		t.Errorf("跨度文案不对：想要 %q，实际 %q", "1 小时 10 分", span)
	}

	// When the last kill is later than "now" (clock skew and similar edge cases), the end
	// takes the later one and must not shrink
	later, _ := m.killTrend(base)
	if len(later) != 1 {
		t.Errorf("现在早于最后一杀时应画 1 格，实际 %d", len(later))
	}
}

// The hour-average curve is a rolling window: each point = kills in the last hour up to
// the end of that bucket, with the last bucket clipped to "now". The rightmost value must
// therefore equal the stats panel's "kills in the last hour".
func TestKillTrendRolling(t *testing.T) {
	kill := func(hour, min int) bountyEvent {
		return bountyEvent{t: time.Date(2026, 9, 11, hour, min, 0, 0, time.UTC), credits: 1, shipType: "eagle"}
	}
	evts := []bountyEvent{
		kill(0, 55), // 00:50 bucket
		kill(1, 5),  // 01:00 bucket
		kill(1, 35), // 01:30 bucket
		kill(1, 55), // 01:50 bucket
	}
	now := time.Date(2026, 9, 11, 2, 0, 0, 0, time.UTC)

	pts, _ := (&monitor{bounties: evts}).killTrend(now)
	if len(pts) != 8 { // 00:50 -> 02:00
		t.Fatalf("应画 8 格，实际 %d", len(pts))
	}
	// Per-bucket window = (bucket end - 1h, bucket end]; the last bucket's end is clipped to now
	want := []int{1, 2, 2, 2, 3, 3, 3, 3}
	for i, w := range want {
		if pts[i].KillsHour != w {
			t.Errorf("第 %d 格（%s）最近 1 小时击杀应为 %d，实际 %d（全部：%v）",
				i, pts[i].TimeLocal, w, pts[i].KillsHour, pts)
		}
	}

	// The last point must agree with the stats panel: (now-1h, now] holds 3 kills (01:05 / 01:35 / 01:55)
	cut := now.Add(-time.Hour)
	var stat int
	for _, b := range evts {
		if b.t.After(cut) && !b.t.After(now) {
			stat++
		}
	}
	if pts[len(pts)-1].KillsHour != stat {
		t.Errorf("末点应与统计窗口一致：面板 %d，曲线 %d", stat, pts[len(pts)-1].KillsHour)
	}

	// The rate curve rolls too: the last point = kills in [now-10min, now); only the 01:55 entry here
	if pts[len(pts)-1].Kills != 1 {
		t.Errorf("末点最近 10 分钟击杀应为 1，实际 %d（全部：%v）", pts[len(pts)-1].Kills, pts)
	}
	// An ordinary bucket's window is that bucket itself; it must not shift because the algorithm changed
	wantKills := []int{1, 1, 0, 0, 1, 0, 1, 1} // 00:50 / 01:00 / - / - / 01:30 / - / 01:50 / last bucket rolling
	for i, w := range wantKills {
		if pts[i].Kills != w {
			t.Errorf("第 %d 格（%s）10 分钟击杀应为 %d，实际 %d", i, pts[i].TimeLocal, w, pts[i].Kills)
		}
	}
}

func TestSpanText(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0 秒"},
		{30 * time.Second, "30 秒"},
		{10 * time.Minute, "10 分钟"},
		{time.Hour, "1 小时"},
		{3*time.Hour + 20*time.Minute, "3 小时 20 分"},
		{25 * time.Hour, "25 小时"},
		{-time.Minute, "0 秒"},
	}
	for _, c := range cases {
		if got := spanText(c.d); got != c.want {
			t.Errorf("spanText(%v) = %q，想要 %q", c.d, got, c.want)
		}
	}
}

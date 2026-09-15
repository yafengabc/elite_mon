package main

import (
	"fmt"
	"testing"
	"time"
)

// This pins down two distinct boundaries; do not conflate them:
//
//   - "total kills / total bounty" aggregates the whole table and must be the true
//     session total, unaffected by any in-memory cap;
//   - "bounty records / message lines" are for display, truncated to
//     cfg.MaxListLen (most recent entries only).
//
// They once shared a single maxBacklog = 5000 cap, which silently undercounted the
// totals in very long sessions. n must be **greater than that former cap**, or the
// test data never reaches the truncation point and the regression is not pinned -
// the trap hit when the first version used 250 (it still PASSed with the old cap
// behavior injected).
func TestTotalsIgnoreMaxListLen(t *testing.T) {
	oldMax := cfg.MaxListLen
	cfg.MaxListLen = 10 // display list set far below the internal record count
	defer func() { cfg.MaxListLen = oldMax }()

	const n = 12000 // > the former maxBacklog (5000)
	base := time.Now().UTC().Add(-4 * time.Hour)

	m := &monitor{}
	for i := 0; i < n; i++ {
		m.bounties = append(m.bounties, bountyEvent{
			t:        base.Add(time.Duration(i) * time.Second),
			credits:  1000,
			shipType: "eagle",
		})
		m.messages = append(m.messages, fmt.Sprintf("kill-%03d", i))
	}

	var st AppStatus
	m.collect(&st, nil, false)

	// Aggregate view: all n session records, independent of MaxListLen
	if st.TotalKills != n {
		t.Errorf("TotalKills=%d，想要 %d —— 总数不该被列表长度截断", st.TotalKills, n)
	}
	if want := int64(n * 1000); st.TotalBounty != want {
		t.Errorf("TotalBounty=%d，想要 %d —— 总数不该被列表长度截断", st.TotalBounty, want)
	}

	// Display view: still only the most recent MaxListLen entries
	if len(st.BountyRecords) != cfg.MaxListLen {
		t.Errorf("BountyRecords=%d 条，想要 %d（对外展示仍需截断）",
			len(st.BountyRecords), cfg.MaxListLen)
	}
	if len(st.MessageLines) != cfg.MaxListLen {
		t.Errorf("MessageLines=%d 条，想要 %d（对外展示仍需截断）",
			len(st.MessageLines), cfg.MaxListLen)
	}
	if got, want := st.BountyRecords[len(st.BountyRecords)-1].Credits, int64(1000); got != want {
		t.Errorf("展示列表末条 credits=%d，想要 %d（应取最近的那批）", got, want)
	}
}

// collect's snapshot must not share a backing array with the monitor's internal
// slices.
//
// Pinning this implicit contract: statusLock only guards replacement of the
// snapshot pointer, not the array contents - the monitor goroutine appends without
// holding it. Today this happens to be safe because the writer's append lands right
// of the read window, but in-place shifting of the old array
// (copy(s, s[len(s)-n:])) would push the writer into the read window and
// go test -race would report DATA RACE.
func TestMessageLinesSnapshotIsCopy(t *testing.T) {
	oldMax := cfg.MaxListLen
	cfg.MaxListLen = 200
	defer func() { cfg.MaxListLen = oldMax }()

	m := &monitor{}
	for i := 0; i < 300; i++ {
		m.messages = append(m.messages, fmt.Sprintf("line-%03d", i))
	}

	var st AppStatus
	m.collect(&st, nil, false)

	if len(st.MessageLines) != 200 {
		t.Fatalf("MessageLines=%d 条，想要 200", len(st.MessageLines))
	}
	if st.MessageLines[0] != "line-100" {
		t.Errorf("应取最后 200 条：首条 %q，想要 line-100", st.MessageLines[0])
	}
	if &st.MessageLines[0] == &m.messages[100] {
		t.Errorf("MessageLines 与 m.messages 共享底层数组 —— 监控协程 append 会写进 HTTP 正在读的区间")
	}
}

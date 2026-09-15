package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Rebuilds m.bounties from real local journals (same order as handle() in production).
// Real logs make a better regression case: they interleave mission rewards, span
// sessions dozens of hours long, and contain the 13 out-of-order timestamps among
// 387411 events - synthetic data rarely covers any of that.
func loadRealBounties(path string) (m *monitor, lastTS time.Time, kills, missions int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, lastTS, 0, 0, err
	}
	defer f.Close()

	m = &monitor{}
	dec := json.NewDecoder(f)
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		line, _ := json.Marshal(raw)
		evt, ok := decodeEvent(string(line))
		if !ok {
			continue
		}
		vt, e := time.Parse(journalTimeLayout, evt.Timestamp)
		if e != nil {
			continue
		}
		switch evt.Event {
		case "Bounty":
			m.bounties = append(m.bounties, bountyEvent{
				t:        vt,
				credits:  evt.reward(),
				shipType: firstNonEmpty(evt.TargetLocalised, evt.Target, "未知"),
			})
			kills++
		case "MissionCompleted":
			if evt.Reward > 0 {
				m.bounties = append(m.bounties, bountyEvent{
					t: vt, credits: evt.Reward, shipType: T("bounty.mission"), mission: true,
				})
				missions++
			}
		}
		if vt.After(lastTS) {
			lastTS = vt
		}
	}
	return m, lastTS, kills, missions, nil
}

// The new single-cursor implementation must match the reference (copy + sort) point for
// point on real logs. Auto-skips when the machine has no journals, so the test still
// passes on other machines.
func TestKillTrendMatchesReferenceOnRealJournals(t *testing.T) {
	dir := filepath.Join(os.Getenv("USERPROFILE"),
		"Saved Games", "Frontier Developments", "Elite Dangerous")
	files, err := filepath.Glob(filepath.Join(dir, "Journal.*.log"))
	if err != nil || len(files) == 0 {
		t.Skipf("本机没有 Journal 目录（%s），跳过", dir)
	}

	var checked, badFiles, badFields, totalPoints int
	var detail []string

	for _, fp := range files {
		m, last, kills, missions, err := loadRealBounties(fp)
		if err != nil || last.IsZero() || kills+missions == 0 {
			continue
		}
		got, gspan := m.killTrend(last)      // new implementation
		want, wspan := refKillTrend(m, last) // reference implementation
		checked++
		totalPoints += len(want)

		if gspan != wspan || len(got) != len(want) {
			badFiles++
			detail = append(detail, filepath.Base(fp)+" 点数/跨度不一致")
			continue
		}
		var d int
		for i := range want {
			if got[i] != want[i] {
				d++
				if d <= 2 {
					detail = append(detail, filepath.Base(fp)+" 第"+want[i].TimeLocal+"格 不一致")
				}
			}
		}
		if d > 0 {
			badFiles++
			badFields += d
		}
	}

	if checked == 0 {
		t.Skipf("本机 %d 份日志里没有可比的会话，跳过", len(files))
	}
	t.Logf("真实日志比对：%d 个会话 / %d 个数据点，不一致会话 %d 个、字段 %d 处",
		checked, totalPoints, badFiles, badFields)
	for i, s := range detail {
		if i >= 10 {
			t.Logf("  ... 另有 %d 条", len(detail)-10)
			break
		}
		t.Logf("  %s", s)
	}
	if badFiles > 0 {
		t.Errorf("新实现与参照实现在 %d 个真实会话上结果不一致", badFiles)
	}
}

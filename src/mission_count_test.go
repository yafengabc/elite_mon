package main

import (
	"testing"
	"time"
)

// TestMissionProgress verifies the mission-progress algorithm: in the last
// Missions list, entries with Expires==0 count as done; a MissionRedirected
// received after that list (for a mission that was active) adds one more.
// Duplicate redirects and already-done missions are not counted twice.
func TestMissionProgress(t *testing.T) {
	m := &monitor{}

	// Last Missions list (real data from the user, 20 missions)
	m.handle(JournalEvent{
		Event: "Missions",
		Active: []MissionActive{
			{MissionID: 1065481805, Expires: 0},
			{MissionID: 1065481823, Expires: 0},
			{MissionID: 1065482327, Expires: 0},
			{MissionID: 1065482335, Expires: 0},
			{MissionID: 1065482352, Expires: 0},
			{MissionID: 1065482369, Expires: 0},
			{MissionID: 1065482375, Expires: 0},
			{MissionID: 1065482428, Expires: 0},
			{MissionID: 1065482601, Expires: 0},
			{MissionID: 1065495663, Expires: 0},
			{MissionID: 1065495671, Expires: 0},
			{MissionID: 1065495685, Expires: 0},
			{MissionID: 1065496106, Expires: 0},
			{MissionID: 1065496111, Expires: 563057},
			{MissionID: 1065496118, Expires: 77499},
			{MissionID: 1065496122, Expires: 563057},
			{MissionID: 1065496132, Expires: 563057},
			{MissionID: 1065496180, Expires: 563194},
			{MissionID: 1065496194, Expires: 563057},
			{MissionID: 1065496213, Expires: 563057},
		},
	}, time.Time{}, &shieldDrop{})

	if m.missionsTotal != 20 {
		t.Fatalf("missionsTotal=%d want 20", m.missionsTotal)
	}
	if m.missionsDoneBase != 13 {
		t.Fatalf("missionsDoneBase=%d want 13", m.missionsDoneBase)
	}

	// 3 MissionRedirected after the list (completions), for 3 previously active missions
	for _, id := range []int64{1065496111, 1065496118, 1065496122} {
		m.handle(JournalEvent{Event: "MissionRedirected", MissionID: id}, time.Time{}, &shieldDrop{})
	}

	done := m.missionsDoneBase + len(m.missionRedirected)
	if done != 16 {
		t.Fatalf("done=%d want 16", done)
	}
	if m.missionsTotal-done != 4 {
		t.Fatalf("not-done=%d want 4", m.missionsTotal-done)
	}

	// Redirecting the same mission again must not double count
	m.handle(JournalEvent{Event: "MissionRedirected", MissionID: 1065496111}, time.Time{}, &shieldDrop{})
	if m.missionsDoneBase+len(m.missionRedirected) != 16 {
		t.Fatalf("duplicate MissionRedirected was counted")
	}

	// Redirecting a mission that already has Expires==0 (awaiting delivery) must not count
	m.handle(JournalEvent{Event: "MissionRedirected", MissionID: 1065481805}, time.Time{}, &shieldDrop{})
	if m.missionsDoneBase+len(m.missionRedirected) != 16 {
		t.Fatalf("already-done mission was counted via redirect")
	}
}

// TestMissionAcceptedAndCompleted verifies incremental events: MissionAccepted
// bumps the total by 1; MissionCompleted drops the total by 1 and the done count
// by 1.
func TestMissionAcceptedAndCompleted(t *testing.T) {
	m := &monitor{}

	// Baseline: 2 missions, 1 awaiting delivery (Expires==0), 1 active
	m.handle(JournalEvent{Event: "Missions", Active: []MissionActive{
		{MissionID: 1, Expires: 0},
		{MissionID: 2, Expires: 100},
	}}, time.Time{}, &shieldDrop{})

	if m.missionsTotal != 2 || m.missionsDoneBase != 1 {
		t.Fatalf("baseline: total=%d done=%d want 2/1", m.missionsTotal, m.missionsDoneBase)
	}

	// Accept a new mission: total +1
	m.handle(JournalEvent{Event: "MissionAccepted", MissionID: 3}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 3 {
		t.Fatalf("after accept: total=%d want 3", m.missionsTotal)
	}
	// Duplicate events are not counted twice
	m.handle(JournalEvent{Event: "MissionAccepted", MissionID: 3}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 3 {
		t.Fatalf("duplicate accept: total=%d want 3", m.missionsTotal)
	}

	// The newly accepted mission gets redirected -> counts as done: 3/2
	m.handle(JournalEvent{Event: "MissionRedirected", MissionID: 3}, time.Time{}, &shieldDrop{})
	if d := m.missionsDoneBase + len(m.missionRedirected); d != 2 {
		t.Fatalf("after redirect: done=%d want 2", d)
	}

	// Deliver an "awaiting delivery" mission: total and done each -1 -> 2/1
	m.handle(JournalEvent{Event: "MissionCompleted", MissionID: 1}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 2 || m.missionsDoneBase+len(m.missionRedirected) != 1 {
		t.Fatalf("after complete(done): total=%d done=%d want 2/1",
			m.missionsTotal, m.missionsDoneBase+len(m.missionRedirected))
	}

	// Deliver a "completed via redirect" mission: total and done each -1 -> 1/0
	m.handle(JournalEvent{Event: "MissionCompleted", MissionID: 3}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 1 || m.missionsDoneBase+len(m.missionRedirected) != 0 {
		t.Fatalf("after complete(redirected): total=%d done=%d want 1/0",
			m.missionsTotal, m.missionsDoneBase+len(m.missionRedirected))
	}

	// Counters must not go negative
	m.handle(JournalEvent{Event: "MissionCompleted", MissionID: 99}, time.Time{}, &shieldDrop{})
	m.handle(JournalEvent{Event: "MissionCompleted", MissionID: 99}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 0 || m.missionsDoneBase < 0 {
		t.Fatalf("underflow: total=%d done=%d", m.missionsTotal, m.missionsDoneBase)
	}
}

// TestMissionCompletedReward verifies mission rewards feed into bounty stats:
// the amount counts toward total/window bounty but not toward kills (kills only
// come from Bounty events).
func TestMissionCompletedReward(t *testing.T) {
	m := &monitor{}
	now := time.Now().UTC()

	m.handle(JournalEvent{
		Event: "Missions",
		Active: []MissionActive{
			{MissionID: 1, Expires: 0},
			{MissionID: 2, Expires: 100},
		},
	}, now, &shieldDrop{})

	// The bounty log only shows "mission reward", never the original mission name
	m.handle(JournalEvent{
		Event:     "MissionCompleted",
		MissionID: 1,
		Reward:    500000,
	}, now, &shieldDrop{})

	// Donation missions have no reward and must not produce a record
	m.handle(JournalEvent{Event: "MissionCompleted", MissionID: 2, Reward: 0}, now, &shieldDrop{})

	if len(m.bounties) != 1 {
		t.Fatalf("bounties=%d want 1 (zero-reward mission must be skipped)", len(m.bounties))
	}
	if !m.bounties[0].mission || m.bounties[0].shipType != T("bounty.mission") {
		t.Fatalf("record=%+v want mission bounty labelled %q", m.bounties[0], T("bounty.mission"))
	}

	var st AppStatus
	m.collect(&st, nil, false)

	if st.TotalKills != 0 {
		t.Fatalf("TotalKills=%d want 0 (mission reward is not a kill)", st.TotalKills)
	}
	if st.TotalBounty != 500000 || st.TotalMissionReward != 500000 {
		t.Fatalf("total bounty=%d mission=%d want 500000/500000",
			st.TotalBounty, st.TotalMissionReward)
	}
	if st.HourKills != 0 || st.HourBounty != 500000 || st.HourMissionReward != 500000 {
		t.Fatalf("hour kills=%d bounty=%d mission=%d want 0/500000/500000",
			st.HourKills, st.HourBounty, st.HourMissionReward)
	}
	if len(st.BountyRecords) != 1 || !st.BountyRecords[0].IsMission {
		t.Fatalf("bounty_records=%+v want 1 mission record", st.BountyRecords)
	}

	// One real kill: kill count rises, mission reward subtotal unchanged
	m.handle(JournalEvent{Event: "Bounty", Timestamp: now.UTC().Format(journalTimeLayout),
		Rewards: []BountyReward{{Reward: 10000}}}, now, &shieldDrop{})
	m.collect(&st, nil, false)

	if st.TotalKills != 1 || st.TotalBounty != 510000 || st.TotalMissionReward != 500000 {
		t.Fatalf("after bounty: kills=%d total=%d mission=%d want 1/510000/500000",
			st.TotalKills, st.TotalBounty, st.TotalMissionReward)
	}
}

// TestMissionFailedAndAbandoned verifies failure/abandonment: total -1, done count
// unchanged (such missions were active, so they never counted as done).
func TestMissionFailedAndAbandoned(t *testing.T) {
	m := &monitor{}

	// Baseline: 3 missions, 1 awaiting delivery, 2 active
	m.handle(JournalEvent{Event: "Missions", Active: []MissionActive{
		{MissionID: 1, Expires: 0},
		{MissionID: 2, Expires: 100},
		{MissionID: 3, Expires: 100},
	}}, time.Time{}, &shieldDrop{})

	// Fail an active mission: 3/1 -> 2/1
	m.handle(JournalEvent{Event: "MissionFailed", MissionID: 2}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 2 || m.missionsDoneBase+len(m.missionRedirected) != 1 {
		t.Fatalf("after fail: total=%d done=%d want 2/1",
			m.missionsTotal, m.missionsDoneBase+len(m.missionRedirected))
	}

	// Abandon one: 2/1 -> 1/1
	m.handle(JournalEvent{Event: "MissionAbandoned", MissionID: 3}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 1 || m.missionsDoneBase+len(m.missionRedirected) != 1 {
		t.Fatalf("after abandon: total=%d done=%d want 1/1",
			m.missionsTotal, m.missionsDoneBase+len(m.missionRedirected))
	}

	// Duplicate events must not drive the total negative
	m.handle(JournalEvent{Event: "MissionFailed", MissionID: 2}, time.Time{}, &shieldDrop{})
	if m.missionsTotal != 0 {
		t.Fatalf("duplicate fail: total=%d want 0", m.missionsTotal)
	}

	// An "awaiting delivery" mission fails on timeout: removed from the done count, so done also -1
	m.handle(JournalEvent{Event: "MissionFailed", MissionID: 1}, time.Time{}, &shieldDrop{})
	if d := m.missionsDoneBase + len(m.missionRedirected); d != 0 {
		t.Fatalf("fail of a waiting-delivery mission: done=%d want 0", d)
	}
}

// TestMissionProgressNewListResets verifies a new Missions event rebuilds the
// baseline: redirects from before the list are ignored (only those after the last
// list count).
func TestMissionProgressNewListResets(t *testing.T) {
	m := &monitor{}

	// Old list: 1 active mission, later completed via redirect
	m.handle(JournalEvent{Event: "Missions", Active: []MissionActive{{MissionID: 1, Expires: 100}}}, time.Time{}, &shieldDrop{})
	m.handle(JournalEvent{Event: "MissionRedirected", MissionID: 1}, time.Time{}, &shieldDrop{})
	if m.missionsDoneBase+len(m.missionRedirected) != 1 {
		t.Fatalf("pre-baseline: done=%d want 1", m.missionsDoneBase+len(m.missionRedirected))
	}

	// New list: that mission is no longer Active (delivered), baseline resets, the old redirect is void
	m.handle(JournalEvent{Event: "Missions", Active: []MissionActive{{MissionID: 2, Expires: 0}}}, time.Time{}, &shieldDrop{})
	done := m.missionsDoneBase + len(m.missionRedirected)
	if done != 1 || m.missionsTotal != 1 {
		t.Fatalf("after new list: done=%d total=%d want 1/1", done, m.missionsTotal)
	}
}

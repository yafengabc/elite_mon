package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeJournalDir creates a fake ED save directory and returns the journal path.
// journalDir() reads USERPROFILE on every call, so overriding the env var here
// redirects it.
//
// Must use t.Setenv, not os.Setenv: the latter does not restore and pollutes later
// tests in the same package. This trap was hit for real - the real-log comparison in
// trend_realdata_test.go got "redirected" to the temp dir and silently skipped,
// looking green while never actually running.
func fakeJournalDir(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "Saved Games", "Frontier Developments", "Elite Dangerous")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("USERPROFILE", root)
	return dir
}

// Ship info from the previous session must not survive a ship swap or relogin.
//
// Regression scenario: the program is running (previous session: Imperial Cutter,
// cargo 512, ship name OLD), the player relogs into a Taipan fighter -> new journal
// -> reset.
//
// Old defect: reset cleared bounties/fuel/cargo but not m.ship; the new session's
// LoadGame carries only the ship type and a numeric FuelCapacity (no CargoCapacity),
// so "empty values do not overwrite" left the previous session's cargo capacity and
// ship name behind. Worse, CargoMax>0 made shipData's "only scan history when
// capacity is missing" condition false, blocking the history backfill too.
func TestReloginClearsShipInfo(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		// Historical journal (previous session): the real mothership, Anaconda
		"Journal.2026-09-10T010000.01.log": `{"timestamp":"2026-09-10T01:00:00Z","event":"Loadout","Ship":"Anaconda","Ship_Localised":"Anaconda","ShipName":"GUAJI","ShipIdent":"YA-14A","CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}` + "\n",
		// Current journal (this session): logged in inside a fighter; LoadGame has no CargoCapacity
		"Journal.2026-09-11T010000.01.log": `{"timestamp":"2026-09-11T01:00:00Z","event":"LoadGame","Ship":"independent_fighter","Ship_Localised":"Taipan","FuelCapacity":0.0}` + "\n",
		// Player is still seated in the fighter - Status.json sets InFighter.
		// That decides which model is displayed; m.ship.Vessel always tracks the mothership.
		"Status.json": `{"Flags":33554432,"Fuel":{"FuelMain":0,"FuelReservoir":0.5}}`,
	})

	m := &monitor{}

	// -- Previous session's state (Imperial Cutter / 512 / ship name OLD) --
	var st AppStatus
	m.collect(&st, []string{
		`{"timestamp":"2026-09-10T22:00:00Z","event":"Loadout","Ship":"Cutter","Ship_Localised":"Imperial Cutter","ShipName":"OLD","ShipIdent":"XX-99","CargoCapacity":512,"FuelCapacity":{"Main":64.0,"Reserve":1.07}}`,
	}, false)
	if m.ship.CargoMax != 512 || m.ship.ShipName != "OLD" {
		t.Fatalf("前置条件没建立：cargoMax=%d shipName=%q", m.ship.CargoMax, m.ship.ShipName)
	}

	// -- Relogin into the fighter: exercise the real readJournal + collect --
	path, lines, reset := m.readJournal()
	if !reset {
		t.Fatalf("切换 journal 应触发 reset，实际 reset=false（当前 %s）", filepath.Base(path))
	}
	m.collect(&st, lines, reset)

	sd := m.shipData()

	// 1) The displayed vehicle follows Status.json: in a fighter shows the fighter
	if sd.Vessel != "Taipan" {
		t.Errorf("船型应为 Taipan，实际 %q", sd.Vessel)
	}
	// 1b) But the mothership model itself must not be overwritten by fighter events -
	// once overwritten it can never be restored (the VehicleSwitch back to the
	// mothership carries no ship type)
	if m.ship.Vessel != "Anaconda" {
		t.Errorf("母舰型号被舰载机 LoadGame 污染：m.ship.Vessel=%q（应为 Anaconda）", m.ship.Vessel)
	}
	// 2) Previous session's values must not linger
	if sd.CargoMax == 512 || sd.ShipName == "OLD" {
		t.Errorf("仍在残留上一局数据：cargoMax=%d shipName=%q", sd.CargoMax, sd.ShipName)
	}
	// 3) Capacities should be genuinely recovered from the historical journal
	if sd.CargoMax != 468 || sd.FuelMainMax != 32 {
		t.Errorf("历史回溯没生效：cargoMax=%d（想要 468）fuelMax=%.0f（想要 32）", sd.CargoMax, sd.FuelMainMax)
	}
	// 4) Ship name / ident should also come from that real Loadout in history
	if sd.ShipName != "GUAJI" || sd.ShipIdent != "YA-14A" {
		t.Errorf("船名/船号没从历史补齐：name=%q ident=%q", sd.ShipName, sd.ShipIdent)
	}
}

// With no historical journal to fill in from, honestly show 0 (unknown) rather than
// leaving the previous session's numbers there pretending to be current state.
func TestReloginNoHistoryShowsZero(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		"Journal.2026-09-11T010000.01.log": `{"timestamp":"2026-09-11T01:00:00Z","event":"LoadGame","Ship":"independent_fighter","Ship_Localised":"Taipan","FuelCapacity":0.0}` + "\n",
	})

	m := &monitor{}
	var st AppStatus
	m.collect(&st, []string{
		`{"timestamp":"2026-09-10T22:00:00Z","event":"Loadout","Ship":"Cutter","Ship_Localised":"Imperial Cutter","ShipName":"OLD","CargoCapacity":512,"FuelCapacity":{"Main":64.0,"Reserve":1.07}}`,
	}, false)

	_, lines, reset := m.readJournal()
	m.collect(&st, lines, reset)

	sd := m.shipData()
	if sd.CargoMax != 0 {
		t.Errorf("无历史可补时货舱上限应为 0，实际 %d", sd.CargoMax)
	}
	if sd.ShipName == "OLD" {
		t.Errorf("船名仍在残留上一局：%q", sd.ShipName)
	}
}

// Real scenario (bug reported 2026-09-11): relogging while seated in a fighter, then
// switching back to the mothership a minute later, left the panel showing Taipan even
// though the player was flying an Anaconda.
//
// Root-cause chain:
//  1. LoadGame's Ship is the current vehicle, so relogging in a fighter reports the SLF
//     model (Independent_Fighter), and updateShip unconditionally wrote it into
//     m.ship.Vessel, which should hold the mothership model;
//  2. switching back to the mothership only emits VehicleSwitch{"To":"Mothership"} -
//     **no ship type** - and no new Loadout follows;
//  3. so m.ship.Vessel stayed "Taipan" forever while Status.json was already
//     InMainShip; the display logic reads m.ship.Vessel and reported the fighter.
func TestSwitchBackToMothershipAfterFighterRelogin(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		// The mothership's Loadout lives in a historical journal and Ship_Localised is
		// missing - ED really does omit that field for anaconda, so fall back to the
		// internal ID and fix the casing.
		"Journal.2026-09-10T190332.01.log": `{"timestamp":"2026-09-10T16:36:47Z","event":"Loadout","Ship":"anaconda","ShipName":"GUAJI","ShipIdent":"YA-14A","CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}` + "\n",
		// Current session: relogin seated in the fighter -> switch back to the mothership.
		// Event sequence copied from a real journal.
		"Journal.2026-09-11T175421.01.log": strings.Join([]string{
			`{"timestamp":"2026-09-11T09:58:01Z","event":"LoadGame","Ship":"Independent_Fighter","Ship_Localised":"Taipan","ShipName":"","ShipIdent":"","FuelLevel":0.0,"FuelCapacity":0.0}`,
			`{"timestamp":"2026-09-11T09:59:10Z","event":"VehicleSwitch","To":"Mothership"}`,
		}, "\n") + "\n",
		// Already back in the mothership: InMainShip set, InFighter / InSRV both clear (real Flags value)
		"Status.json": `{"Flags":16842764,"Fuel":{"FuelMain":32.0,"FuelReservoir":0.62}}`,
	})

	m := &monitor{}
	var st AppStatus
	_, lines, reset := m.readJournal()
	m.collect(&st, lines, reset)

	sd := m.shipData()
	if sd.Vessel != "Anaconda" {
		t.Errorf("切回母舰后应显示母舰型号 Anaconda，实际 %q", sd.Vessel)
	}
	if sd.InFighter {
		t.Errorf("Status.json 是 InMainShip，不该判定为在舰载机里")
	}
	if sd.CargoMax != 468 || sd.FuelMainMax != 32 {
		t.Errorf("母舰容量没从历史补回来：cargoMax=%d（想要 468）fuelMax=%.0f（想要 32）",
			sd.CargoMax, sd.FuelMainMax)
	}
	if sd.ShipName != "GUAJI" || sd.ShipIdent != "YA-14A" {
		t.Errorf("船名/船号没补齐：name=%q ident=%q", sd.ShipName, sd.ShipIdent)
	}
}

// Launching a fighter from the mothership (a Loadout with Ship=SLF) must likewise not
// overwrite the mothership model.
func TestFighterLoadoutKeepsMothershipVessel(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		"Journal.2026-09-11T010000.01.log": strings.Join([]string{
			`{"timestamp":"2026-09-11T01:00:00Z","event":"Loadout","Ship":"anaconda","Ship_Localised":"Anaconda","ShipName":"GUAJI","ShipIdent":"YA-14A","CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}`,
			`{"timestamp":"2026-09-11T01:10:00Z","event":"Loadout","Ship":"independent_fighter","Ship_Localised":"Taipan","CargoCapacity":0,"FuelCapacity":{"Main":0,"Reserve":0}}`,
			`{"timestamp":"2026-09-11T01:20:00Z","event":"VehicleSwitch","To":"Mothership"}`,
		}, "\n") + "\n",
		"Status.json": `{"Flags":16842764}`,
	})

	m := &monitor{}
	var st AppStatus
	_, lines, reset := m.readJournal()
	m.collect(&st, lines, reset)

	if m.ship.Vessel != "Anaconda" {
		t.Errorf("母舰型号被舰载机 Loadout 顶掉了：%q", m.ship.Vessel)
	}
	if m.fighterVessel != "Taipan" {
		t.Errorf("舰载机型号没记下来：%q", m.fighterVessel)
	}
	if sd := m.shipData(); sd.Vessel != "Anaconda" {
		t.Errorf("切回母舰后应显示 Anaconda，实际 %q", sd.Vessel)
	}
}

// If the last Loadout in a historical journal is a fighter (logged out inside one),
// keep scanning backwards for the mothership.
func TestScanLoadoutSkipsFighter(t *testing.T) {
	dir := fakeJournalDir(t, map[string]string{
		// The earlier one holds the mothership
		"Journal.2026-09-09T010000.01.log": `{"timestamp":"2026-09-09T01:00:00Z","event":"Loadout","Ship":"anaconda","Ship_Localised":"Anaconda","ShipName":"GUAJI","ShipIdent":"YA-14A","CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}` + "\n",
		// The newer one's last Loadout is a fighter (player logged out inside it)
		"Journal.2026-09-10T010000.01.log": strings.Join([]string{
			`{"timestamp":"2026-09-10T01:00:00Z","event":"Loadout","Ship":"anaconda","Ship_Localised":"Anaconda","CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}`,
			`{"timestamp":"2026-09-10T02:00:00Z","event":"Loadout","Ship":"independent_fighter","Ship_Localised":"Taipan","CargoCapacity":0,"FuelCapacity":{"Main":0,"Reserve":0}}`,
		}, "\n") + "\n",
		"Journal.2026-09-11T010000.01.log": `{"timestamp":"2026-09-11T01:00:00Z","event":"LoadGame","Ship":"Independent_Fighter","Ship_Localised":"Taipan","FuelCapacity":0.0}` + "\n",
		"Status.json":                      `{"Flags":16842764}`,
	})

	m := &monitor{}
	var st AppStatus
	_, lines, reset := m.readJournal()
	m.collect(&st, lines, reset)

	if ok := m.scanLoadout(filepath.Join(dir, "Journal.2026-09-10T010000.01.log")); !ok {
		t.Fatalf("该文件里有母舰 Loadout，不该返回 false")
	}
	if m.ship.Vessel != "Anaconda" {
		t.Errorf("应跳过舰载机 Loadout 找到母舰型号，实际 %q", m.ship.Vessel)
	}
	if m.ship.CargoMax != 468 || m.ship.FuelMainMax != 32 {
		t.Errorf("容量补错了：cargoMax=%d fuelMax=%.0f", m.ship.CargoMax, m.ship.FuelMainMax)
	}
}

// displayShipName is the fallback when Ship_Localised is missing.
func TestDisplayShipName(t *testing.T) {
	cases := map[string]string{
		"anaconda":    "Anaconda",
		"panthermkii": "Panther Clipper Mk II",
		"krait_mkii":  "Krait Mk II",
		// Case-insensitive: ShipyardSwap's StoreOldShip writes Krait_MkII / CobraMkV
		"Krait_MkII": "Krait Mk II",
		"CobraMkV":   "Cobra Mk V",
		// Unknown entries fall back to capitalizing the first letter, at least not all-lowercase
		"unknown_ship": "Unknown_ship",
		"":             "",
		"sidewinder":   "Sidewinder",
	}
	for in, want := range cases {
		if got := displayShipName(in); got != want {
			t.Errorf("displayShipName(%q) = %q，想要 %q", in, got, want)
		}
	}
}

// Real scenario (bug reported 2026-09-11): swapping from an Anaconda to a Krait Mk II
// at the shipyard left the panel showing the internal ID "krait_mkii" with the previous
// ship's name GUAJI still attached.
//
// Root causes:
//  1. the ShipyardSwap event was not handled, so the swap only landed via the Loadout
//     3 seconds later;
//  2. Loadout **carries no Ship_Localised**, so updateShip wrote the internal ID;
//  3. a freshly swapped ship has an empty ShipName, and "empty values do not overwrite"
//     kept the old name.
func TestShipyardSwapUpdatesShip(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		"Journal.2026-09-11T175421.01.log": strings.Join([]string{
			// Starts out flying an Anaconda
			`{"timestamp":"2026-09-11T09:00:00Z","event":"Loadout","Ship":"anaconda","ShipName":"GUAJI","ShipIdent":"YA-14A","ShipID":55,"CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}`,
			// Shipyard swap to a Krait Mk II: ShipyardSwap carries ShipType_Localised but no capacities
			`{"timestamp":"2026-09-11T10:36:31Z","event":"ShipyardSwap","ShipType":"krait_mkii","ShipType_Localised":"Krait Mk II","ShipID":19,"StoreOldShip":"Anaconda","StoreShipID":55}`,
			// Loadout fills in capacities 3 seconds later; note the missing Ship_Localised and empty ShipName
			`{"timestamp":"2026-09-11T10:36:34Z","event":"Loadout","Ship":"krait_mkii","ShipID":19,"ShipName":"","ShipIdent":"YA-07K","CargoCapacity":96,"FuelCapacity":{"Main":32.0,"Reserve":0.63}}`,
		}, "\n") + "\n",
		"Status.json": `{"Flags":16842764,"Fuel":{"FuelMain":30.0,"FuelReservoir":0.39}}`,
	})

	m := &monitor{}
	var st AppStatus
	_, lines, reset := m.readJournal()
	m.collect(&st, lines, reset)

	sd := m.shipData()
	// 1) The ship type must show a human-readable name, not the internal ID
	if sd.Vessel != "Krait Mk II" {
		t.Errorf("换船后船型应为 %q，实际 %q", "Krait Mk II", sd.Vessel)
	}
	// 2) The previous ship's name must be cleared (the new ship was never named)
	if sd.ShipName != "" {
		t.Errorf("换船后不该残留旧船名，实际 %q", sd.ShipName)
	}
	// 3) Ship ident / capacities follow the new ship
	if sd.ShipIdent != "YA-07K" {
		t.Errorf("船号应为 YA-07K，实际 %q", sd.ShipIdent)
	}
	if sd.CargoMax != 96 || sd.FuelMainMax != 32 {
		t.Errorf("容量没跟到新船：cargoMax=%d（想要 96）fuelMax=%.0f（想要 32）",
			sd.CargoMax, sd.FuelMainMax)
	}
}

// When only ShipyardSwap has arrived and the Loadout has not come through yet, the ship
// type must still switch immediately. Capacities are unknown at that point, so showing 0
// briefly is better than keeping the previous ship's cargo / fuel numbers.
func TestShipyardSwapAloneSwitchesVessel(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		"Journal.2026-09-11T010000.01.log": `{"timestamp":"2026-09-11T01:00:00Z","event":"Loadout","Ship":"anaconda","ShipName":"GUAJI","ShipID":55,"CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}` + "\n",
		"Status.json":                      `{"Flags":16842764}`,
	})

	m := &monitor{}
	var st AppStatus
	m.collect(&st, []string{
		`{"timestamp":"2026-09-11T10:36:31Z","event":"ShipyardSwap","ShipType":"krait_mkii","ShipType_Localised":"Krait Mk II","ShipID":19,"StoreOldShip":"Anaconda","StoreShipID":55}`,
	}, false)

	if m.ship.Vessel != "Krait Mk II" {
		t.Errorf("ShipyardSwap 就该立刻换船型，实际 %q", m.ship.Vessel)
	}
	if m.ship.CargoMax != 0 || m.ship.ShipName != "" {
		t.Errorf("旧船数据没清干净：cargoMax=%d shipName=%q", m.ship.CargoMax, m.ship.ShipName)
	}
}

// ShipyardSwap sometimes gives only ShipType without ShipType_Localised (that is the
// case for anaconda); the lookup table must fill in "Anaconda".
func TestShipyardSwapWithoutLocalised(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		"Journal.2026-09-11T010000.01.log": `{"timestamp":"2026-09-11T01:00:00Z","event":"Loadout","Ship":"krait_mkii","ShipName":"","ShipIdent":"YA-07K","ShipID":19,"CargoCapacity":96,"FuelCapacity":{"Main":32.0,"Reserve":0.63}}` + "\n",
		"Status.json":                      `{"Flags":16842764}`,
	})

	m := &monitor{}
	var st AppStatus
	m.collect(&st, []string{
		`{"timestamp":"2026-09-11T16:36:40Z","event":"ShipyardSwap","ShipType":"anaconda","ShipID":55,"StoreOldShip":"Krait_MkII","StoreShipID":19}`,
	}, false)

	if m.ship.Vessel != "Anaconda" {
		t.Errorf("缺 ShipType_Localised 时该查表得到 Anaconda，实际 %q", m.ship.Vessel)
	}
}

// A fighter has its own ShipID; it must not be treated as a "ship swap" signal that
// wipes the mothership data.
func TestFighterShipIDDoesNotResetMothership(t *testing.T) {
	fakeJournalDir(t, map[string]string{
		"Journal.2026-09-11T010000.01.log": strings.Join([]string{
			`{"timestamp":"2026-09-11T01:00:00Z","event":"Loadout","Ship":"anaconda","ShipName":"GUAJI","ShipIdent":"YA-14A","ShipID":55,"CargoCapacity":468,"FuelCapacity":{"Main":32.0,"Reserve":1.07}}`,
			// Launching a fighter from the mothership: ShipID is the fighter's own (56), not the mothership's 55
			`{"timestamp":"2026-09-11T01:10:00Z","event":"Loadout","Ship":"independent_fighter","Ship_Localised":"Taipan","ShipID":56,"CargoCapacity":0,"FuelCapacity":{"Main":0,"Reserve":0}}`,
			`{"timestamp":"2026-09-11T01:20:00Z","event":"VehicleSwitch","To":"Mothership"}`,
		}, "\n") + "\n",
		"Status.json": `{"Flags":16842764}`,
	})

	m := &monitor{}
	var st AppStatus
	_, lines, reset := m.readJournal()
	m.collect(&st, lines, reset)

	if m.fighterVessel != "Taipan" {
		t.Errorf("舰载机型号没记下来：%q", m.fighterVessel)
	}
	// Key point: mothership data must stay untouched
	if m.ship.CargoMax != 468 || m.ship.ShipName != "GUAJI" || m.ship.ShipID != 55 {
		t.Errorf("舰载机的 ShipID 把母舰数据清掉了：cargoMax=%d shipName=%q shipID=%d",
			m.ship.CargoMax, m.ship.ShipName, m.ship.ShipID)
	}
	if sd := m.shipData(); sd.Vessel != "Anaconda" {
		t.Errorf("切回母舰后应显示 Anaconda，实际 %q", sd.Vessel)
	}
}

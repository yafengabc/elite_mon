package main

// Display timezone (config.toml "timezone").
//
// Journal timestamps are always UTC (ED's "game time" is UTC), so display must
// convert to a human-friendly zone. A hard-coded +8 constant (tzOffset) was
// wrong on another machine or user; it is now config-driven, and the default is
// still UTC+8 (identical to the old behavior).
//
// Accepted forms (case-insensitive; unrecognized input falls back to UTC+8 with
// a log line):
//
//	(empty) / 8 / +8 / UTC+8 / 北京时间         → fixed offset (default)
//	UTC / utc / Z / 游戏时间                     → UTC
//	自动 / auto / local / 本机                   → local timezone
//	UTC-5 / UTC+5:30 / -3.5                      → fixed offset (hours may be fractional)
//
// The non-English words come from the language tables (langSpec.tzWords); each
// language can register its own.
//
// Applied once at startup, then read-only: the hot path (fmtTime runs for every
// bounty record) reads the zone via an atomic pointer — no locks, and it still
// works if a runtime switch is added later.

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// defaultTZMinutes is the default offset (UTC+8); the old tzOffset constant.
const defaultTZMinutes = 8 * 60

var displayLoc atomic.Pointer[time.Location]

// displayLocation returns the current display zone; before config is applied it
// uses the default UTC+8.
func displayLocation() *time.Location {
	if l := displayLoc.Load(); l != nil {
		return l
	}
	return time.FixedZone(utcOffsetText(defaultTZMinutes), defaultTZMinutes*60)
}

// applyTimeZone sets the display zone from the config value and logs one line.
// Unrecognized input falls back to UTC+8 — a bad timezone must not stop startup
// (the old behavior was a hard-coded +8).
func applyTimeZone(spec string) {
	loc, label, ok := parseTimeZone(spec)
	if !ok {
		log.Print(T("log.tz_unknown", strconv.Quote(spec)))
		loc, label = displayLocation(), utcOffsetText(defaultTZMinutes)
	}
	displayLoc.Store(loc)
	log.Println(T("log.tz_display", label))
}

// tzWords holds extra timezone words contributed by the language tables (see
// langSpec.tzWords); registerLang fills it at init.
var tzWords = map[string]string{}

// tzAlias resolves a timezone word to a canonical form ("auto" / "utc" /
// "beijing"). The English words are built in; other languages bring their own
// from their table, so e.g. a Japanese user can type 自動 without touching this
// file. Returns "" when the word is not a keyword (it is then parsed as an
// offset).
func tzAlias(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "local", "system":
		return "auto"
	case "utc", "z", "gmt", "utc+0", "utc-0", "utc0", "edt":
		return "utc"
	case "cst":
		return "beijing"
	}
	return tzWords[strings.ToLower(strings.TrimSpace(s))]
}

// parseTimeZone parses a timezone spec, returning the zone, a human-readable
// label, and whether it was recognized.
func parseTimeZone(spec string) (*time.Location, string, bool) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return time.FixedZone(utcOffsetText(defaultTZMinutes), defaultTZMinutes*60),
			utcOffsetText(defaultTZMinutes), true
	}
	switch tzAlias(s) {
	case "auto":
		_, off := time.Now().Zone()
		return time.Local, T("tz.auto_local", utcOffsetText(off/60)), true
	case "utc":
		return time.UTC, "UTC", true
	case "beijing":
		return time.FixedZone(utcOffsetText(480), 480*60), utcOffsetText(480), true
	}

	// Everything else is parsed as "[UTC|GMT] ±H[:MM]".
	body := strings.ToLower(s)
	for _, p := range []string{"utc", "gmt"} {
		if strings.HasPrefix(body, p) {
			body = strings.TrimSpace(body[len(p):])
			break
		}
	}
	if body == "" { // a bare "UTC" is handled above; fall back to default here
		return time.FixedZone(utcOffsetText(defaultTZMinutes), defaultTZMinutes*60),
			utcOffsetText(defaultTZMinutes), true
	}
	neg := false
	switch body[0] {
	case '-':
		neg, body = true, body[1:]
	case '+':
		body = body[1:]
	}

	var minutes int
	if i := strings.IndexByte(body, ':'); i >= 0 {
		h, err1 := strconv.Atoi(strings.TrimSpace(body[:i]))
		m, err2 := strconv.Atoi(strings.TrimSpace(body[i+1:]))
		if err1 != nil || err2 != nil || h < 0 || m < 0 || m > 59 {
			return nil, "", false
		}
		minutes = h*60 + m
	} else {
		f, err := strconv.ParseFloat(body, 64)
		if err != nil || f < 0 {
			return nil, "", false
		}
		minutes = int(f*60 + 0.5)
	}
	if neg {
		minutes = -minutes
	}
	if minutes < -14*60 || minutes > 14*60 { // UTC±14 is the real-world limit
		return nil, "", false
	}
	return time.FixedZone(utcOffsetText(minutes), minutes*60), utcOffsetText(minutes), true
}

// utcOffsetText renders an offset in minutes as a human-readable label:
// UTC+8 / UTC+5:30 / UTC. It doubles as the FixedZone name, so do not build a
// second representation elsewhere.
func utcOffsetText(minutes int) string {
	if minutes == 0 {
		return "UTC"
	}
	sign := "+"
	if minutes < 0 {
		sign, minutes = "-", -minutes
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("UTC%s%d", sign, minutes/60)
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, minutes/60, minutes%60)
}

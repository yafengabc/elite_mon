package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// baseText is the base language's text for an id. Tests compare against it rather
// than against a literal, so rewording a translation is not a test change.
func baseText(id string) string {
	if sp := langByCode[baseLang]; sp != nil {
		return sp.table[id]
	}
	return ""
}

// A language registered here stands in for a lang/<code>.toml file: it must be
// selectable by its config aliases, its own strings must win, ids it does not
// translate must fall back to the base language, and the panel table must carry
// the merged result.
func TestRegisterLanguageFallbackAndPanelTable(t *testing.T) {
	list, byCode := snapshotLangs()
	defer restoreLangs(list, byCode)
	defer applyLangValue(baseLang)

	registerLang(langSpec{
		code:  "xx",
		tag:   "xx-XX",
		names: []string{"xx", "testish"},
		table: map[string]string{
			"app.title":     "XX Monitor",
			"app.init":      "XX init",
			"tz.auto_local": "XX auto ({0})",
		},
		errWords:  []string{"boom"},
		warnWords: []string{"careful"},
		tzWords:   map[string]string{"xxauto": "auto"},
	})

	if got := parseLang("Testish"); got != "xx" {
		t.Fatalf("parseLang alias: got %q, want xx", got)
	}
	if got := parseLang("klingon"); got != baseLang {
		t.Fatalf("unknown language: got %q, want %q", got, baseLang)
	}
	if got := parseLang(""); got != baseLang {
		t.Fatalf("empty language: got %q, want %q", got, baseLang)
	}

	applyLangValue("xx")
	if got := T("app.title"); got != "XX Monitor" {
		t.Errorf("translated id: got %q", got)
	}
	// Not in the xx table: the base language must fill the gap, not the raw id.
	if got, want := T("tab.status"), baseText("tab.status"); got != want {
		t.Errorf("fallback to %s: got %q, want %q", baseLang, got, want)
	}
	// In no table at all: the id itself, so the gap is visible instead of blank.
	if got := T("no.such.id"); got != "no.such.id" {
		t.Errorf("unknown id: got %q", got)
	}
	if got := langCode(); got != "xx" {
		t.Errorf("langCode: got %q, want xx", got)
	}
	if got := langTag(); got != "xx-XX" {
		t.Errorf("langTag: got %q, want xx-XX", got)
	}

	tbl := panelTable()
	if tbl["app.title"] != "XX Monitor" {
		t.Errorf("panel table must prefer the active language, got %q", tbl["app.title"])
	}
	if tbl["tab.status"] != baseText("tab.status") {
		t.Errorf("panel table must fill gaps from %s, got %q", baseLang, tbl["tab.status"])
	}
	if got := tzAlias("XXAUTO"); got != "auto" {
		t.Errorf("tzWords: tzAlias(XXAUTO) = %q, want auto", got)
	}
}

func TestTSubstitution(t *testing.T) {
	defer applyLangValue(baseLang)
	applyLangValue(baseLang)

	if got, want := T("summary.total_kills", 42), "总击杀：42"; got != want {
		t.Errorf("substitution: got %q, want %q", got, want)
	}
	if got, want := T("common.span_hours", "1.5"), "1.5 小时"; got != want {
		t.Errorf("string argument: got %q, want %q", got, want)
	}
	// A missing argument must leave its placeholder, not truncate the sentence.
	if got, want := T("ship.cargo", 3), "货舱：3 / {1}"; got != want {
		t.Errorf("missing argument: got %q, want %q", got, want)
	}
	if got, want := T("ship.cargo", 3, 8, 9), "货舱：3 / 8"; got != want {
		t.Errorf("extra argument: got %q, want %q", got, want)
	}
	// Percent signs are plain text now that placeholders use braces.
	if got, want := T("ship.main_fuel", 5, 32, "15.6"), "主油箱：5 吨 / 32 吨（15.6%）"; got != want {
		t.Errorf("literal percent: got %q, want %q", got, want)
	}
}

// Every registered table must cover the same id set: a gap means the UI silently
// mixes two languages.
func TestTablesCoverTheSameIds(t *testing.T) {
	base := langByCode[baseLang]
	if base == nil {
		t.Fatal("base language table is not registered")
	}
	if len(langs) < 2 {
		t.Fatalf("expected at least 2 languages, got %d", len(langs))
	}
	for _, sp := range langs {
		for id := range base.table {
			if _, ok := sp.table[id]; !ok {
				t.Errorf("%s: missing id %q", sp.code, id)
			}
		}
		for id := range sp.table {
			if _, ok := base.table[id]; !ok {
				t.Errorf("%s: id %q is missing from %s", sp.code, id, baseLang)
			}
		}
		if sp.tag == "" {
			t.Errorf("%s: no <html lang> tag", sp.code)
		}
		if len(sp.names) == 0 {
			t.Errorf("%s: no config alias", sp.code)
		}
	}
}

// A translator who drops a {1} would silently print half a sentence, and the
// placeholder count is not something a review would reliably catch.
func TestPlaceholdersMatchAcrossLanguages(t *testing.T) {
	base := baseTable()
	ph := regexp.MustCompile(`\{(\d+)\}`)
	if base == nil {
		t.Fatal("base language table is not registered")
	}
	for _, sp := range langs {
		for id, want := range base {
			got, ok := sp.table[id]
			if !ok {
				continue // reported by TestTablesCoverTheSameIds
			}
			a, b := ph.FindAllString(want, -1), ph.FindAllString(got, -1)
			sort.Strings(a)
			sort.Strings(b)
			if strings.Join(a, ",") != strings.Join(b, ",") {
				t.Errorf("%s: %s has placeholders %v, %s has %v",
					sp.code, id, b, baseLang, a)
			}
		}
	}
}

// Timezone words are config input rather than UI text, and each language
// contributes its own; all of them must resolve regardless of the active one.
func TestTZWordsPerLanguage(t *testing.T) {
	for _, tc := range []struct{ word, want string }{
		{"自动", "auto"}, {"AUTO", "auto"}, {"utc", "utc"}, {"游戏时间", "utc"},
		{"北京时间", "beijing"}, {"2026", ""}, {"-5", ""},
	} {
		if got := tzAlias(tc.word); got != tc.want {
			t.Errorf("tzAlias(%q) = %q, want %q", tc.word, got, tc.want)
		}
	}
}

// A log line written by a language's own keywords must still colour correctly,
// so each table has to carry the words its own log lines use.
func TestSeverityWordsFollowTheLanguage(t *testing.T) {
	defer applyLangValue(baseLang)

	applyLangValue(baseLang)
	errWords, _ := severityWords()
	if !containsAny("WxPusher发送失败: x", errWords) {
		t.Error("zh: an error line should match the error keywords")
	}
	// The Chinese table also lists the English words: a log line can carry an
	// untranslated external error whatever the UI language is.
	if !containsAny("Config parse failed", errWords) {
		t.Error("zh: an English error line should still be classified")
	}

	applyLangValue("en")
	errWords, warnWords := severityWords()
	if !containsAny("Config parse failed", errWords) {
		t.Error("en: an error line should match the error keywords")
	}
	// The words must match the wording the English log lines really use.
	if !containsAny("Monitor goroutine panic, skipping round: x", errWords) {
		t.Error("en: the panic line should be classified as an error")
	}
	if !containsAny("Too many WxPusher in-flight requests, dropping push: x", warnWords) {
		t.Error("en: the dropped-push line should be classified as a warning")
	}
}

// --- language files on disk --------------------------------------------------

// Editing a file must be enough to change the text, and a file whose code no
// built-in knows must register a whole new language. The file is also allowed to
// be partial: untranslated ids fall back to the base language.
func TestLanguageFilesOverrideAndAddLanguages(t *testing.T) {
	dir := t.TempDir()
	writeLang(t, dir, "en.toml", `
code  = "en"
tag   = "en"
names = ["en"]
[strings.app]
title = "Overridden title"
`)
	writeLang(t, dir, "xx.toml", `
code  = "xx"
tag   = "xx-XX"
names = ["xx", "testish"]
[tz_words]
"xxauto" = "auto"
[strings.app]
title = "XX Monitor"
[strings.tab]
status = "XX Status"
`)

	specs, _ := loadLanguages(dir)

	en := findSpec(t, specs, "en")
	if got := en.table["app.title"]; got != "Overridden title" {
		t.Errorf("disk override: got %q", got)
	}
	// An id the edited file does not carry keeps the built-in English text - it
	// must not fall through to the base language.
	if got, want := en.table["panel.title"], "ED Real-time Monitor Panel"; got != want {
		t.Errorf("same-language fill: got %q, want %q", got, want)
	}

	xx := findSpec(t, specs, "xx")
	if _, ok := xx.table["panel.title"]; ok {
		t.Error("an id the new language does not translate must not be invented")
	}
	if got := xx.tzWords["xxauto"]; got != "auto" {
		t.Errorf("tz_words from the file: got %q, want auto", got)
	}

	// Register what the loader returned and drive the engine with it.
	list, byCode := snapshotLangs()
	defer restoreLangs(list, byCode)
	defer applyLangValue(baseLang)
	for _, sp := range specs {
		registerLang(sp)
	}

	applyLangValue("testish")
	if got := T("app.title"); got != "XX Monitor" {
		t.Errorf("new language by alias: got %q", got)
	}
	if got := T("tab.status"); got != "XX Status" {
		t.Errorf("new language: got %q", got)
	}
	// Not translated yet: the base language fills the gap.
	if got, want := T("panel.title"), baseText("panel.title"); got != want {
		t.Errorf("untranslated id: got %q, want the %s text %q", got, baseLang, want)
	}
	if got := tzAlias("XXAUTO"); got != "auto" {
		t.Errorf("tz_words from the file via the engine: got %q", got)
	}
}

// A fresh install has no lang/ directory: the built-in files must be written out
// so there is an editable copy, and reading them back must not lose anything.
func TestMissingLangDirIsPopulated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lang")

	specs, notices := loadLanguages(dir)
	for _, code := range []string{baseLang, "en"} {
		sp := findSpec(t, specs, code)
		if _, err := os.Stat(filepath.Join(dir, code+langExt)); err != nil {
			t.Errorf("%s.toml was not written out: %v", code, err)
		}
		if want := langByCode[code]; want != nil && len(sp.table) != len(want.table) {
			t.Errorf("%s: %d ids loaded from the written file, %d are built in",
				code, len(sp.table), len(want.table))
		}
	}
	if !mentionsLang(notices, dir) {
		t.Errorf("creating the language directory should be reported, got %v", evalNotices(notices))
	}

	// A second load reads the files instead of writing them: same content.
	again, second := loadLanguages(dir)
	if len(second) != 0 {
		t.Errorf("an intact lang/ directory should produce no messages, got %v",
			evalNotices(second))
	}
	for _, code := range []string{baseLang, "en"} {
		if a, b := findSpec(t, specs, code).table, findSpec(t, again, code).table; len(a) != len(b) {
			t.Errorf("%s: %d ids before, %d after the round trip", code, len(a), len(b))
		}
	}
}

// A file that does not parse must not break its language: the built-in text
// keeps working and the problem is reported. It must not also be reported as
// missing, which would send the user looking for a file that is right there.
func TestBrokenLanguageFileFallsBackToBuiltIn(t *testing.T) {
	dir := t.TempDir()
	writeLang(t, dir, "en.toml", "code = \"en\"\n[strings.app]\ntitle = \"unterminated\n")

	specs, notices := loadLanguages(dir)
	en := findSpec(t, specs, "en")
	if got, want := en.table["app.title"], "ED Real-time Monitor"; got != want {
		t.Errorf("broken file must fall back to the built-in text: got %q, want %q", got, want)
	}
	txt := strings.Join(evalNotices(notices), "\n")
	if !strings.Contains(txt, "en.toml") {
		t.Errorf("a broken language file must be reported, got %v", txt)
	}
	// An absent file is reported by listing it, so a wrongly-reported en.toml
	// would show up as exactly this message.
	if strings.Contains(txt, T("log.lang_file_missing", "en.toml")) {
		t.Errorf("a file that exists but does not parse must not be called missing: %v", txt)
	}
}

// A stale or mis-typed file is reported: how much of the base language it does
// not carry, and how many entries the program does not know.
func TestLanguageFileProblemsAreReported(t *testing.T) {
	dir := t.TempDir()
	writeLang(t, dir, "yy.toml", `
code = "yy"
[strings.app]
title   = "YY Monitor"
typo_id = "oops"
`)

	specs, notices := loadLanguages(dir)
	yy := findSpec(t, specs, "yy")
	if got := yy.table["app.typo_id"]; got != "oops" {
		t.Fatalf("an unknown id is still loaded, and then reported: got %q", got)
	}

	txt := strings.Join(evalNotices(notices), "\n")
	// One id translated, so it is missing len(base)-1; app.typo_id is unknown.
	if want := len(baseTable()) - 1; !strings.Contains(txt, fmt.Sprint(want)) {
		t.Errorf("the untranslated count (%d) should be reported, got:\n%s", want, txt)
	}
	if !strings.Contains(txt, "yy.toml") {
		t.Errorf("the file should be named in the report, got:\n%s", txt)
	}
}

// A translated string must be looked up when it is displayed, never captured in
// a package-level variable or an IIFE initializer: those run before any init(),
// so the language tables are not registered yet and T() returns the raw id.
// This actually happened twice - gui.go froze "tab.status" / "tab.data" into the
// tab strip, and loadConfig() tried to translate its own messages.
//
// Written as a source scan rather than a call into gui.go, so it also covers the
// files excluded by the current build tags. The enclosing declaration of a line
// is the nearest earlier line that starts in column 0 with var / func / type /
// const, which is how Go lays declarations out.
func TestNoTranslatedStringFrozenAtInit(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("cannot list package sources: %v", err)
	}

	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		decl, declLine := "", 0
		for i, raw := range strings.Split(string(data), "\n") {
			line := raw
			if p := strings.Index(line, "//"); p >= 0 {
				line = line[:p] // a T() in a comment does not count
			}
			if raw != "" && raw[0] != ' ' && raw[0] != '\t' && i > 0 {
				decl, declLine = strings.TrimSpace(raw), i
			}
			if !strings.Contains(line, "T(") || strings.HasPrefix(line, "//") {
				continue
			}
			if strings.HasPrefix(decl, "var ") || strings.HasPrefix(decl, "var\t") {
				bad = append(bad, fmt.Sprintf("%s:%d (inside %s:%d %q)",
					f, i+1, f, declLine+1, decl))
			}
		}
	}

	if len(bad) > 0 {
		t.Errorf("T() is evaluated during package init and will return raw ids:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// --- helpers ----------------------------------------------------------------

func writeLang(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findSpec(t *testing.T, specs []langSpec, code string) langSpec {
	t.Helper()
	for _, sp := range specs {
		if sp.code == code {
			return sp
		}
	}
	t.Fatalf("language %q was not loaded", code)
	return langSpec{}
}

// evalNotices renders the deferred log lines; the tables are registered by the
// time a test runs, so they can be resolved here.
func evalNotices(notices []pendingLog) []string {
	out := make([]string, 0, len(notices))
	for _, n := range notices {
		out = append(out, n())
	}
	return out
}

func mentionsLang(notices []pendingLog, sub string) bool {
	for _, n := range evalNotices(notices) {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func baseTable() map[string]string {
	if sp := langByCode[baseLang]; sp != nil {
		return sp.table
	}
	return nil
}

func snapshotLangs() ([]*langSpec, map[string]*langSpec) {
	byCode := make(map[string]*langSpec, len(langByCode))
	for k, v := range langByCode {
		byCode[k] = v
	}
	return append([]*langSpec(nil), langs...), byCode
}

func restoreLangs(list []*langSpec, byCode map[string]*langSpec) {
	langs = list
	langByCode = byCode
}

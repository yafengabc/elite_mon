package main

// i18n.go - UI language engine.
//
// Text is looked up by a stable ASCII id ("<area>.<name>"), never by the display
// text: with the Chinese text as the key, rewording a single Chinese string made
// every other language silently fall back to Chinese.
//
// Tables live one TOML file per language (lang/<code>.toml), loaded by
// i18n_lang.go: the translations are data, so they can be edited or added
// without recompiling. The web panel keeps no table of its own either - it pulls
// the active one from /api/i18n, so each language is translated in exactly one
// place.
//
// Adding a language:
//  1. copy lang/en.toml to lang/<code>.toml, beside the program;
//  2. set code / names / tag in it - names are the values accepted in
//     config.toml's "language", matched case-insensitively;
//  3. translate the [strings] values (the ids are stable, do not rename them);
//  4. list the error / warning substrings that should colour a log line red or
//     yellow in that language, and optionally its own timezone spellings.
//
// Nothing else needs changing.

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// baseLang is the fallback language. An id missing from the active table is
// looked up here, so a partly translated language still shows readable text.
const baseLang = "zh"

// langSpec describes one language table.
type langSpec struct {
	code      string            // "en" - also the code sent to the web panel
	tag       string            // BCP-47 tag for the panel's <html lang> (font/line-break hints)
	names     []string          // config.toml "language" values, lowercase
	table     map[string]string // id -> display text
	errWords  []string          // logSeverity: substrings meaning "error"
	warnWords []string          // logSeverity: substrings meaning "warning"
	tzWords   map[string]string // optional: extra timezone words -> canonical
}

var (
	langs       []*langSpec // registration order
	langByCode  = map[string]*langSpec{}
	currentSpec atomic.Pointer[langSpec]
)

// registerLang adds a language; the loader in i18n_lang.go calls it once per
// language found, in a stable order.
func registerLang(s langSpec) {
	sp := s
	langs = append(langs, &sp)
	langByCode[sp.code] = &sp
	for k, v := range sp.tzWords {
		tzWords[strings.ToLower(k)] = v
	}
}

// curSpec is the active language; before the config is read it is the base
// language.
func curSpec() *langSpec {
	if sp := currentSpec.Load(); sp != nil {
		return sp
	}
	return langByCode[baseLang]
}

// parseLang maps a config.toml "language" value to a registered code. Any value
// matching no code or alias falls back to the base language.
func parseLang(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return baseLang
	}
	for _, sp := range langs {
		if sp.code == v {
			return sp.code
		}
		for _, n := range sp.names {
			if n == v {
				return sp.code
			}
		}
	}
	return baseLang
}

// applyLang applies the configured language.
func applyLang() {
	applyLangValue(cfg.Language)
}

// applyLangValue sets the active language from a raw config value. Callers must
// run after the language tables are registered (i.e. after init(), so any time
// from main() onwards): config loading happens during package init and therefore
// buffers its messages instead of translating them - see cfgNotices.
func applyLangValue(lang string) {
	if sp := langByCode[parseLang(lang)]; sp != nil {
		currentSpec.Store(sp)
	}
}

// langCode is the active language code for the web panel ("zh" / "en").
func langCode() string {
	return curSpec().code
}

// langTag is the active language's BCP-47 tag, used for the panel's <html lang>
// (it steers font fallback and line breaking for CJK).
func langTag() string {
	if sp := curSpec(); sp != nil && sp.tag != "" {
		return sp.tag
	}
	return baseLang
}

// T returns the text for id in the active language, with {0}, {1}, ... replaced
// by args. An unknown id is returned unchanged, which makes a missing entry
// visible in the UI instead of blank.
func T(id string, args ...any) string {
	s := lookup(id)
	if len(args) == 0 {
		return s
	}
	return format(s, args)
}

func lookup(id string) string {
	if sp := curSpec(); sp != nil {
		if v, ok := sp.table[id]; ok {
			return v
		}
	}
	if base := langByCode[baseLang]; base != nil && base != curSpec() {
		if v, ok := base.table[id]; ok {
			return v
		}
	}
	return id
}

// format substitutes positional placeholders: the text holds {0}, {1}, ... and
// args hold the values. Positional braces are used rather than fmt verbs because
// the same table feeds the web panel, where fmt verbs are unavailable; callers
// pass numbers already formatted, so neither side needs locale-specific number
// handling.
func format(tpl string, args []any) string {
	if !strings.Contains(tpl, "{") {
		return tpl
	}
	var b strings.Builder
	b.Grow(len(tpl) + 16)
	for i := 0; i < len(tpl); {
		if tpl[i] != '{' {
			b.WriteByte(tpl[i])
			i++
			continue
		}
		j := strings.IndexByte(tpl[i:], '}')
		if j < 0 {
			b.WriteString(tpl[i:])
			break
		}
		n, err := strconv.Atoi(tpl[i+1 : i+j])
		if err != nil || n < 0 || n >= len(args) {
			b.WriteString(tpl[i : i+j+1]) // not a placeholder: keep it verbatim
			i += j + 1
			continue
		}
		b.WriteString(fmt.Sprint(args[n]))
		i += j + 1
	}
	return b.String()
}

// panelTable returns the whole id -> text map for the web panel: the base
// language overlaid by the active one, so the front end needs no fallback logic.
func panelTable() map[string]string {
	out := make(map[string]string, len(langs)*160)
	if base := langByCode[baseLang]; base != nil {
		for k, v := range base.table {
			out[k] = v
		}
	}
	for k, v := range curSpec().table {
		out[k] = v
	}
	return out
}

// severityWords returns the error / warning substrings of the active language,
// used by logSeverity to colour the log box.
func severityWords() (errWords, warnWords []string) {
	sp := curSpec()
	if sp == nil {
		return nil, nil
	}
	return sp.errWords, sp.warnWords
}

// containsAny reports whether line contains any of words.
func containsAny(line string, words []string) bool {
	for _, w := range words {
		if strings.Contains(line, w) {
			return true
		}
	}
	return false
}

package main

// locale.go - the "auto" UI language.
//
// config.toml's "language" defaults to auto: the program follows the system
// locale instead of making every user on a Chinese machine edit a file to get
// English (or the other way round).
//
// A system locale is resolved in this order:
//
//  1. the OS language (Windows: the display language, falling back to the user
//     locale - e.g. "zh-CN", "en-US");
//  2. only when the OS says nothing, the POSIX variables LC_ALL / LC_MESSAGES /
//     LANG ("en_US.UTF-8" -> "en-us").
//
// The OS is consulted first on Windows on purpose: Git Bash exports
// LANG=en_US.UTF-8, so preferring the variable would hand English to a user
// whose machine is Chinese - exactly the surprise this feature must avoid.
//
// The locale is then matched against the registered languages - the full tag
// first (so "zh-TW" may have a file of its own), then the primary subtag
// ("zh-TW" -> the zh file). A locale nobody claims, e.g. a Japanese system with
// no ja.toml, falls back to the base language, the same rule an unrecognised
// explicit value gets.
//
// Accepted for auto: "auto", "system", "自动", "跟随系统", or an omitted key.

import (
	"os"
	"strings"
)

// autoLangWords are the config.toml "language" values meaning "follow the
// system". The empty string is one of them: an omitted key takes the default,
// and the default is auto.
var autoLangWords = []string{"", "auto", "system", "自动", "跟随系统"}

// isAutoLang reports whether a raw "language" value asks for detection rather
// than naming a language.
func isAutoLang(v string) bool {
	w := strings.ToLower(strings.TrimSpace(v))
	for _, a := range autoLangWords {
		if w == a {
			return true
		}
	}
	return false
}

// systemLocaleFn is the locale lookup used by auto; a variable so a test can
// drive the automatic path without touching the machine's own settings.
var systemLocaleFn = systemLocale

// systemLocale returns the locale to detect a language from, or "" when the
// machine does not say. See the file comment for why the OS wins over LANG.
func systemLocale() string {
	if v := normalizeLocale(osLocale()); v != "" {
		return v
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := normalizeLocale(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// normalizeLocale turns a raw locale into a comparable tag: "en_US.UTF-8" ->
// "en-us", "zh-CN" -> "zh-cn". The C and POSIX locales mean "no preference".
func normalizeLocale(v string) string {
	if i := strings.IndexAny(v, ".@"); i >= 0 {
		v = v[:i]
	}
	v = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(v), "_", "-"))
	if v == "c" || v == "posix" {
		return ""
	}
	return v
}

// matchLocale maps a locale to a registered language code, or the base language
// when nothing matches.
func matchLocale(locale string) string {
	v := normalizeLocale(locale)
	if v == "" {
		return baseLang
	}
	primary := v
	if i := strings.IndexByte(v, '-'); i > 0 {
		primary = v[:i]
	}
	for _, want := range []string{v, primary} {
		for _, sp := range langs {
			if sp.code == want || strings.EqualFold(sp.tag, want) || hasName(sp, want) {
				return sp.code
			}
		}
	}
	return baseLang
}

// hasName reports whether sp lists w among the config values it answers to.
func hasName(sp *langSpec, w string) bool {
	for _, n := range sp.names {
		if strings.EqualFold(n, w) {
			return true
		}
	}
	return false
}

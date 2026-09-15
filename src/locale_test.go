package main

// locale_test.go - the "auto" language path (see locale.go).
//
// The system locale is stubbed rather than read, so the expectations hold on any
// machine and on any of the build variants these tests are run for.

import (
	"strings"
	"testing"
)

// withLocale makes the automatic path see a fixed system locale for one test.
func withLocale(t *testing.T, locale string) {
	t.Helper()
	saved := systemLocaleFn
	systemLocaleFn = func() string { return locale }
	t.Cleanup(func() { systemLocaleFn = saved })
}

func TestAutoLanguageFollowsTheSystemLocale(t *testing.T) {
	defer applyLangValue(baseLang)

	cases := []struct {
		locale string
		want   string
	}{
		{"zh-cn", "zh"},
		{"zh", "zh"},
		{"zh-TW", "zh"}, // region nobody claims: the primary subtag decides
		{"en", "en"},
		{"en-US", "en"},
		{"en-GB", "en"},
		{"ja-JP", baseLang}, // no ja.toml: same fallback an unknown value gets
		{"", baseLang},      // the machine names no locale at all
	}
	for _, c := range cases {
		withLocale(t, c.locale)
		notice := applyLangValue("auto")
		if got := langCode(); got != c.want {
			t.Errorf("locale %q: language = %q, want %q", c.locale, got, c.want)
		}
		switch {
		case c.locale == "" && notice != nil:
			t.Errorf("locale %q: unexpected notice", c.locale)
		case c.locale != "" && notice == nil:
			t.Errorf("locale %q: no notice, so the choice would be invisible", c.locale)
		}
	}
}

func TestAutoNoticeIsTranslatedAndFilledIn(t *testing.T) {
	defer applyLangValue(baseLang)

	for _, c := range []struct{ locale, wantWord string }{
		{"en-US", "UI language"},
		{"zh-CN", "界面语言"},
	} {
		withLocale(t, c.locale)
		notice := applyLangValue("auto")
		if notice == nil {
			t.Fatalf("locale %q: no notice", c.locale)
		}
		// The notice is built after the language is applied, so it must already
		// read in the language auto picked - and carry no leftovers.
		got := notice()
		if !strings.Contains(got, c.wantWord) {
			t.Errorf("locale %q: notice %q is not in the detected language", c.locale, got)
		}
		if !strings.Contains(got, c.locale) {
			t.Errorf("locale %q: notice %q does not name the locale", c.locale, got)
		}
		if strings.ContainsAny(got, "{}") {
			t.Errorf("locale %q: unsubstituted placeholder in %q", c.locale, got)
		}
	}
}

func TestExplicitLanguageBeatsTheSystemLocale(t *testing.T) {
	defer applyLangValue(baseLang)

	for _, c := range []struct{ locale, value, want string }{
		{"en-US", "中文", "zh"},
		{"zh-CN", "English", "en"},
		{"en-US", "zh", "zh"},
	} {
		withLocale(t, c.locale)
		if notice := applyLangValue(c.value); notice != nil {
			t.Errorf("locale %q value %q: an explicit choice must not report an auto pick",
				c.locale, c.value)
		}
		if got := langCode(); got != c.want {
			t.Errorf("locale %q value %q: language = %q, want %q", c.locale, c.value, got, c.want)
		}
	}
}

func TestAutoLanguageWords(t *testing.T) {
	for _, v := range []string{"", "auto", "AUTO", "  system  ", "自动", "跟随系统"} {
		if !isAutoLang(v) {
			t.Errorf("%q should mean auto", v)
		}
	}
	for _, v := range []string{"en", "english", "中文", "zh", "en-US", "ja"} {
		if isAutoLang(v) {
			t.Errorf("%q should name a language, not auto", v)
		}
	}
}

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"en_US.UTF-8":   "en-us",
		"zh-CN":         "zh-cn",
		"ja_JP@euro":    "ja-jp",
		"  EN  ":        "en",
		"C":             "",
		"POSIX":         "",
		"":              "",
		"zh-Hans-CN":    "zh-hans-cn",
		"de_DE.utf8":    "de-de",
		"pt_BR.ISO8859": "pt-br",
	}
	for in, want := range cases {
		if got := normalizeLocale(in); got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

// matchLocale is the pure half of the detection: it must not depend on the stub.
func TestMatchLocaleAgainstRegisteredLanguages(t *testing.T) {
	cases := map[string]string{
		"zh-cn":   "zh",
		"zh-tw":   "zh",
		"en-us":   "en",
		"english": "en",
		"chinese": "zh",
		"en_US":   "en",
		"fr-FR":   baseLang,
		"":        baseLang,
		"swahili": baseLang,
	}
	for in, want := range cases {
		if got := matchLocale(in); got != want {
			t.Errorf("matchLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

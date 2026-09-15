package main

// i18n_lang.go - loads the UI language files.
//
// Every language is one `lang/<code>.toml` file next to config.toml. The files
// are plain TOML so they can be edited and added without recompiling:
//
//   - the built-in copies are embedded in the binary, which keeps a fresh
//     install working with no lang/ directory at all;
//   - on first run (when lang/ does not exist yet) the built-ins are written
//     out, so there is always an editable copy to start from;
//   - a file found on disk wins key by key over its built-in twin, so a partial
//     or edited file behaves predictably;
//   - built-in ids missing from an edited file are filled from the built-in
//     twin (same language), and only then from the base language - an outdated
//     file degrades to shipped text rather than to another language;
//   - a `lang/<code>.toml` whose code is new simply adds a language: set that
//     code in config.toml's "language" and restart.
//
// The loader runs from init() and never logs directly: the text it would log
// comes from the very tables it is loading, so messages are returned as
// pendingLog values and printed by main() once the language is known.

import (
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// builtinLangFS holds the shipped language files; see the doc comment above.
//
//go:embed lang/*.toml
var builtinLangFS embed.FS

const (
	langDirName = "lang"
	langExt     = ".toml"
)

// langNotices are the language-loading messages, printed by main() after
// applyLang() - they cannot be translated any earlier.
var langNotices []pendingLog

func init() {
	specs, notices := loadLanguages(langDir())
	for _, sp := range specs {
		registerLang(sp)
	}
	langNotices = notices
}

// langFile is one language file, on disk or embedded.
type langFile struct {
	Code      string                       `toml:"code"`
	Tag       string                       `toml:"tag"`
	Names     []string                     `toml:"names"`
	ErrWords  []string                     `toml:"err_words"`
	WarnWords []string                     `toml:"warn_words"`
	TZWords   map[string]string            `toml:"tz_words"`
	Strings   map[string]map[string]string `toml:"strings"`

	// keys the decoder did not recognise, i.e. a mis-spelled field name.
	unknownKeys []string
}

// table flattens the [strings.<area>] sections into the "<area>.<name>" ids the
// code looks up.
func (f langFile) table() map[string]string {
	out := make(map[string]string, 200)
	for area, kv := range f.Strings {
		for k, v := range kv {
			out[area+"."+k] = v
		}
	}
	return out
}

// toSpec turns a loaded file into the engine's language record. builtin is the
// shipped twin of the same code (zero value for a language that only exists as
// a file): it fills whatever the file leaves out, so hand-editing a file down to
// a handful of lines still yields a usable language.
func (f langFile) toSpec(builtin langFile) langSpec {
	tbl := f.table()
	for k, v := range builtin.table() {
		if _, ok := tbl[k]; !ok {
			tbl[k] = v
		}
	}
	spec := langSpec{
		code:      firstNonEmpty(f.Code, builtin.Code),
		tag:       firstNonEmpty(f.Tag, builtin.Tag),
		names:     orStrings(f.Names, builtin.Names),
		table:     tbl,
		errWords:  orStrings(f.ErrWords, builtin.ErrWords),
		warnWords: orStrings(f.WarnWords, builtin.WarnWords),
		tzWords:   f.TZWords,
	}
	if len(spec.tzWords) == 0 {
		spec.tzWords = builtin.TZWords
	}
	return spec
}

// langDir is the editable language directory: beside config.toml, so ED_CONFIG
// moves it too and an isolated smoke test stays isolated.
func langDir() string {
	return filepath.Join(filepath.Dir(configPath()), langDirName)
}

// loadLanguages reads the built-in files and every file in dir, and returns the
// languages to register plus any messages to log. It does not touch the registry,
// which keeps it callable from tests.
func loadLanguages(dir string) ([]langSpec, []pendingLog) {
	builtin := map[string]langFile{}
	paths, err := fs.Glob(builtinLangFS, langDirName+"/*"+langExt)
	if err != nil {
		return nil, nil
	}
	sort.Strings(paths)
	for _, p := range paths {
		data, err := builtinLangFS.ReadFile(p)
		if err != nil {
			continue
		}
		f, _, err := parseLangFile(data)
		if err != nil {
			// Only reachable when a shipped file is malformed, i.e. a build-time
			// mistake: log() already carries it into the GUI ring.
			log.Println("built-in language file is invalid:", p, err)
			continue
		}
		builtin[codeOf(f, p)] = f
	}

	var notices []pendingLog
	dirExisted := true
	if _, err := os.Stat(dir); err != nil {
		dirExisted = false
		if err = os.MkdirAll(dir, 0o755); err != nil {
			notices = append(notices,
				func() string { return T("log.lang_dir_failed", dir, err) })
			return specsOf(builtin, nil, baseLang), notices
		}
		for _, p := range paths {
			data, err := builtinLangFS.ReadFile(p)
			if err != nil {
				continue
			}
			// A file that cannot be written (read-only dir) is not fatal: the
			// built-in copy still serves that language.
			_ = os.WriteFile(filepath.Join(dir, filepath.Base(p)), data, 0o644)
		}
		notices = append(notices,
			func() string { return T("log.lang_dir_created", dir) })
	}

	disk, present, diskNotices := readDiskLangs(dir)
	notices = append(notices, diskNotices...)
	specs := specsOf(builtin, disk, baseLang)

	// Diagnostics are measured against the base language's id set: those are the
	// ids the code actually asks for.
	baseIDs := builtin[baseLang].table()
	for _, sp := range specs {
		file, onDisk := disk[sp.code]
		if !onDisk {
			continue // shipped text only: nothing to report
		}
		missing, unknown := 0, 0
		for id := range baseIDs {
			if _, ok := file.table()[id]; !ok {
				missing++
			}
		}
		for id := range file.table() {
			if _, ok := baseIDs[id]; !ok {
				unknown++
			}
		}
		unknown += len(file.unknownKeys)
		name := sp.code + langExt
		if missing > 0 {
			n := missing
			notices = append(notices, func() string { return T("log.lang_missing_ids", name, n) })
		}
		if unknown > 0 {
			n := unknown
			notices = append(notices, func() string { return T("log.lang_unknown_ids", name, n) })
		}
	}

	if dirExisted {
		var absent []string
		for _, sp := range specs {
			// Only a language with a built-in twin can be regenerated, and a file
			// that exists but does not parse is already reported as such.
			if builtin[sp.code].Code != "" && !present[sp.code] {
				absent = append(absent, sp.code+langExt)
			}
		}
		if len(absent) > 0 {
			list := strings.Join(absent, ", ")
			notices = append(notices, func() string { return T("log.lang_file_missing", list) })
		}
	}
	return specs, notices
}

// specsOf merges the built-in and on-disk languages: the base language first,
// then alphabetical, so registration order is stable.
func specsOf(builtin, disk map[string]langFile, base string) []langSpec {
	seen := map[string]bool{}
	order := make([]string, 0, len(builtin)+len(disk))
	for _, set := range []map[string]langFile{builtin, disk} {
		for code := range set {
			if !seen[code] {
				seen[code] = true
				order = append(order, code)
			}
		}
	}
	sort.Strings(order)
	if i := indexOf(order, base); i > 0 {
		order = append([]string{base}, append(order[:i:i], order[i+1:]...)...)
	}

	specs := make([]langSpec, 0, len(order))
	for _, code := range order {
		f, ok := disk[code]
		if !ok {
			f = builtin[code]
		}
		specs = append(specs, f.toSpec(builtin[code]))
	}
	return specs
}

// readDiskLangs reads every *.toml in dir. It returns the files that parsed
// (by language code), the codes that exist on disk at all - a file that exists
// but does not parse must not also be reported as missing - and the messages to
// log. A file that does not parse keeps its built-in twin working.
func readDiskLangs(dir string) (parsed map[string]langFile, present map[string]bool, notices []pendingLog) {
	parsed = map[string]langFile{}
	present = map[string]bool{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return parsed, present, nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), langExt) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		present[stem] = true

		data, err := os.ReadFile(path)
		if err == nil {
			var f langFile
			var md toml.MetaData
			f, md, err = parseLangFile(data)
			if err == nil {
				f.unknownKeys = undecodedNames(md)
				code := codeOf(f, path)
				present[code] = true
				parsed[code] = f
				continue
			}
		}
		file, why := name, err
		notices = append(notices, func() string { return T("log.lang_parse_failed", file, why) })
	}
	return parsed, present, notices
}

// parseLangFile decodes one TOML language file. Decode (not Unmarshal) is used
// for the meta data, which is what reports a mis-spelled field name.
func parseLangFile(data []byte) (langFile, toml.MetaData, error) {
	var f langFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return f, md, err
	}
	return f, md, nil
}

// codeOf is the language code of a file: the "code" field if the translator set
// one, otherwise the file name, so a minimal file still registers.
func codeOf(f langFile, path string) string {
	if c := strings.ToLower(strings.TrimSpace(f.Code)); c != "" {
		return c
	}
	return strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
}

// undecodedNames lists TOML keys the struct does not know, which is what a
// mis-spelled field name looks like from here.
func undecodedNames(md toml.MetaData) []string {
	keys := md.Undecoded()
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.String())
	}
	sort.Strings(out)
	return out
}

func orStrings(v, fallback []string) []string {
	if len(v) > 0 {
		return v
	}
	return fallback
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

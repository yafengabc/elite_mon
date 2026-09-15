package main

import (
	"runtime/debug"
	"strings"
	"time"
)

// version is the release tag, injected at build time by build.sh:
//
//	go build -ldflags "-X main.version=v1.0.0"
//
// It stays empty for a plain `go build`, in which case the VCS revision becomes
// the identity instead (see versionInfo).
var version = ""

// versionInfo renders the build identity shown in the About dialog and printed
// at startup: the tag plus the VCS facts Go stamps into the binary by itself
// when building inside a git checkout.
//
//	v1.0.0 (a1b2c3d, 2026-09-15, clean)   tagged release
//	a1b2c3d (2026-09-15, dirty)           untagged local build
//	v1.0.0                                built outside a checkout (tarball)
//	dev                                   neither tag nor VCS information
//
// The date is the *commit* date, not the build time: that keeps a build
// reproducible, and for a release artifact the two are minutes apart anyway.
func versionInfo() string {
	rev, commited, modified := vcsInfo()

	head := version
	if head == "" {
		head = rev // untagged: the revision is the identity
	}

	var extra []string
	// `git describe --tags` can already carry the hash ("v1.0.0-3-ga1b2c3d");
	// in that case printing it again would just be noise.
	if rev != "" && !strings.Contains(head, rev) {
		extra = append(extra, rev)
	}
	if commited != "" {
		extra = append(extra, commited)
	}
	if rev != "" {
		if modified {
			extra = append(extra, "dirty")
		} else {
			extra = append(extra, "clean")
		}
	}

	if head == "" {
		head = "dev"
	}
	if len(extra) == 0 {
		return head
	}
	return head + " (" + strings.Join(extra, ", ") + ")"
}

// vcsInfo reads the stamping the Go toolchain adds when the build runs inside a
// git checkout (on by default: -buildvcs=auto). All three values are empty when
// the sources came from a tarball or a copy with no .git, so every caller must
// treat them as optional.
func vcsInfo() (revision, commitDate string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
			if len(revision) > 7 {
				revision = revision[:7] // short hash, as git prints it
			}
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				commitDate = t.UTC().Format("2006-01-02")
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, commitDate, modified
}

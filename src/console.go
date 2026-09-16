//go:build !gui

package main

// Console build (no GUI): a plain console program on Windows; also
// cross-compiles straight to Linux.
//
// The monitoring core (elite_monitor.go) and the web panel (static/) are shared
// with the GUI build. This file provides just the two entry points, each with a
// mutually exclusive twin in gui.go:
//
//	startLogging — GUI captures logs into its window; console keeps them on stderr
//	runUI        — GUI runs the window message loop; console blocks on the HTTP server
//
// Build:
//
//	go build                native console build (a console-window exe on Windows)
//	GOOS=linux go build     Linux build
//	go build -tags gui      Windows GUI build (see gui.go / win32.go)
//
// Platforms where the GUI build cannot run fall back here — the build tags
// already exclude them.

import (
	"log"
	"net/http"
)

// startLogging is a no-op in the console build: logs belong on stderr anyway.
//
// Kept as a function because the `cfg` initializer in elite_monitor.go depends
// on it for ordering (see the comment there), and a log window only makes sense
// in the GUI build.
func startLogging() {}

// runUI starts the panel server and blocks on it (never returns).
//
// Key difference from the GUI build: the console has no window to show errors,
// so failing to start the server is fatal — exit immediately with a clear reason
// (the old console build used log.Fatal; that behavior is preserved).
//
// With "enable_panel": false it listens on no port and degrades to pure
// monitoring: WxPusher push still works, there is just no panel to view, and the
// main thread then sleeps forever.
func runUI() {
	if !cfg.EnablePanel {
		log.Println(T("log.panel_off_note"))
		select {} // monitoring and push run in their own goroutines; main sleeps here
	}

	log.Println(T("log.panel_addr", portOf(cfg.ListenAddr)))
	// The {0} placeholder has to be filled here: T() leaves it verbatim when the
	// argument is missing, so passing the error to log.Fatalln separately would
	// print "failed to start: {0} <err>".
	log.Fatalln(T("log.panel_start_failed", http.ListenAndServe(cfg.ListenAddr, nil)))
}

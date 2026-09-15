# Elite Dangerous Journal Monitor

[中文说明](README.zh-CN.md) · **English**

A small, self-contained desktop monitor for **Elite Dangerous**. It tails the game's
journal files and turns them into the three things you actually want while playing —
or while *not* playing:

- **Kill & bounty stats** — total kills, total bounty, rolling windows, mission rewards.
- **Shield-drop alerts** — the moment your shields go down, you get a push to WeChat.
- **Log-silence alerts** — if the journal stops growing while you are AFK, the game has
  almost certainly disconnected. You get told, instead of finding out hours later.
- **Plus a web panel** — reachable from your phone or another PC on the same LAN: live
  status, kill-trend chart, bounty log and the raw event stream.

<img src="docs/win32-en.png" alt="Win32 interface" width="720">

## Features

| | |
|---|---|
| **Statistics** | Total kills and bounty, "last N" windows, per-kill bounty log, mission reward breakdown, kill-trend chart (last 10 min / last 1 h), session totals |
| **Alerts** | Shield drop and log silence, pushed through [WxPusher](https://wxpusher.zjiecode.com) to WeChat. Repeated silence alerts are capped so an overnight disconnect cannot spam you |
| **Ship info** | Current ship, name/ID, cargo, main and reserve fuel — recovered from older journals when the current one has no `Loadout` event yet |
| **Web panel** | Live status, trend chart (inline SVG), bounty log, event stream. Served over the LAN, gzip-compressed, no build step, no CDN |
| **Bilingual UI** | 中文 / English, switchable in the config. Translations live in editable TOML files — adding a language needs no recompilation |
| **Self-contained** | Single executable. No installer, no runtime, no DLLs, **no CGO** — the Tk build embeds Tcl/Tk 9.0 as pure Go |
| **Editable config** | Plain TOML next to the executable, with the defaults documented inline |

## Download

Grab the artifact you want from [Releases](../../releases):

| File | Platform | Notes |
|---|---|---|
| `elite_mon_win32.exe` | Windows | **Recommended.** Native Win32 UI — plain Win32 SDK, no third-party UI libraries. Follows the system visual theme; tray icon; closing the window minimizes to the tray |
| `elite_mon_tk.exe` | Windows | Tk 9.0 UI. Useful if you prefer it; no DLLs to ship |
| `elite_mon.exe` | Windows | Console build — no window, logs to stderr |
| `elite_mon_tk_linux_amd64` | Linux | Tk UI |
| `elite_mon_linux_amd64` | Linux | Console |

Both Linux artifacts are **cross-compiled from Windows and verified to build, but have not
been run on a real Linux desktop yet** — the Windows builds are the tested ones. Bug
reports are welcome.

## Quick start

1. Put the executable in its own folder and run it. On first run it writes `config.toml`
   and a `lang/` directory beside itself.
2. Open `config.toml`. If you want WeChat push, fill in your WxPusher credentials:

   ```toml
   [wxpusher]
     url = "https://wxpusher.zjiecode.com/api/send/message"
     app_token = "AT_..."   # your WxPusher app token
     uid = "UID_..."        # your WxPusher user UID
   ```

   Leave them empty and the program still monitors — it simply does not push.
3. Restart the program. That's it: the panel is on `http://localhost:8088`, and on
   `http://<your-lan-ip>:8088` from another device.

The monitor finds your journals automatically:

```
%USERPROFILE%\Saved Games\Frontier Developments\Elite Dangerous\
```

> **Keep `config.toml` private.** It holds a push credential that can send messages to
> your WeChat. It is git-ignored here for that reason.

## Configuration

`config.toml` is read once at startup — restart to apply changes. Duration fields accept
`30s` / `5m` / `1h`; deleting the file regenerates it from the built-in defaults.

| Key | Default | Meaning |
|---|---|---|
| `listen_addr` | `":8088"` | Panel listen address. Use `"127.0.0.1:8088"` to keep it off the LAN |
| `timezone` | `"UTC+8"` | Display timezone. Accepts `UTC+8`, `UTC`, `auto`, `UTC+5:30`, `北京时间` |
| `enable_panel` | `true` | `false` never opens a port — monitoring and push still run |
| `language` | `"中文"` | `中文` or `English` |
| `poll_interval` | `"2s"` | How often the journal is re-read |
| `stall_threshold` | `"10m"` | Push an alert once the journal has been silent this long |
| `stat_window` | `"1h"` | Window used for the "last N" statistics |
| `max_list_len` | `200` | Maximum records returned per API call |
| `history_scan_count` | `5` | Historical journals to scan when ship data is missing |
| `wxpusher.url` | WxPusher endpoint | Push service endpoint |
| `wxpusher.app_token` | — | Your WxPusher app token |
| `wxpusher.uid` | — | Your WxPusher user UID |

## Web panel

<img src="docs/panel-en.png" alt="Web panel" width="620">

Two endpoints back it:

- `GET /api/status` — everything the panel renders (statistics, ship state, bounty
  records, kill trend, recent event lines). Gzip-compressed.
- `GET /api/i18n` — the active language table, so the panel and the desktop UI can never
  disagree about wording.

The panel serves static files from the binary itself and is scaled to a fixed virtual
viewport, so it looks the same on a phone as on a desktop.

## Interfaces

<table>
<tr>
<td><img src="docs/win32-zh.png" alt="Win32, Chinese" width="380"></td>
<td><img src="docs/tk-en.png" alt="Tk" width="380"></td>
</tr>
<tr>
<td align="center">Win32 (中文)</td>
<td align="center">Tk</td>
</tr>
</table>

| Build tag | Result |
|---|---|
| *(none)* | Console |
| `gui` on Windows | Native Win32 UI (`src/win32.go` + `src/gui.go`) |
| `gui` on Linux, or `gui,tk` on Windows | Tk UI (`src/tk.go`) |

All variants share the monitoring core and the embedded frontend; only the two entry
points differ (`startLogging` and `runUI`).

## Language files

Translations live in `src/lang/*.toml`, embedded into the binary and extracted to `lang/`
next to your config on first run. Editing a file and restarting is enough — **there is
nothing to recompile**:

```toml
[strings.panel]
title = "ED Real-time Monitor Panel"
```

To add a language, copy `lang/en.toml` to `lang/ja.toml`, set `code` / `tag` / `names`,
translate the `[strings]` tables, and put `language = "ja"` in `config.toml`. Any id you
leave out falls back to the base language, so a half-finished translation still works.
Two extra fields control how log lines are coloured:

```toml
err_words  = ["失敗", "エラー"]   # a line containing these is shown red
warn_words = ["警告"]             # ... and these, yellow
```

## Building from source

Requires [Go](https://go.dev/dl/) 1.26 or newer. **No CGO, no gcc, no MSYS2** — every
variant builds from a plain toolchain.

```sh
git clone <this repo>
cd elite_mon_ui

./build.sh all     # console + Win32 + Tk + both Linux cross-builds → Release/
./build.sh check   # gofmt + vet + test across every variant
```

On Windows the script fetches [`rsrc`](https://github.com/akavel/rsrc) automatically to
compile the visual-style manifest and the app icon into the executable. That manifest is
what makes Windows draw the widgets with the system theme instead of Windows 95 styling,
so always build through `./build.sh` — a bare `go build ./src` produces a binary with no
manifest and no icon.

The version shown in Help → About comes from `git describe --tags` plus the revision and
commit date the Go toolchain stamps in by itself (`v1.0.0 (a1b2c3d, 2026-09-15, clean)`).
Building outside a git checkout reports `dev`.

### Project layout

```
src/                  Go sources (single main package) + embedded frontend
  ├─ elite_monitor.go   journal parsing, statistics, HTTP panel
  ├─ version.go         build-identity reporting
  ├─ i18n.go            translation engine (ids, fallback, formatting)
  ├─ i18n_lang.go       loads src/lang/*.toml, extracts them beside the config
  ├─ lang/*.toml        translations (embedded; editable at runtime)
  ├─ static/            web panel: index.html, style.css, main.js (//go:embed)
  ├─ icon/ app.manifest program icon and Windows visual-style manifest
  ├─ win32.go gui.go    Win32 UI          console.go  console entry points
  └─ tk.go              Tk UI
tools/                dev-time helpers (make_icon.py, migrate_config.py)
build.sh              build / check / upx / clean
```

## Releases

Pushing a tag matching `v*` triggers CI (`windows-latest`, no compiler needed) to run
`./build.sh all` and attach all five artifacts, plus `SHA256SUMS.txt`, to the GitHub
Release.

## Notes

- **Unofficial.** Not affiliated with or endorsed by Frontier Developments. *Elite
  Dangerous* is a trademark of Frontier Developments plc.
- Push delivery uses [WxPusher](https://wxpusher.zjiecode.com), a third-party service that
  delivers via a WeChat service account. It is not an official WeChat API.
- Journal timestamps are UTC; everything displayed goes through the configured timezone.

## License

[MIT](LICENSE)

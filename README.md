# Elite Dangerous Journal Monitor

[中文说明](README.zh-CN.md) · **English**

A small, self-contained desktop monitor for **Elite Dangerous**. It tails the game's
journal files and turns them into the three things you actually want while playing —
or while *not* playing:

- **Kill & bounty stats** — total kills, total bounty, rolling windows, mission rewards.
- **Shield-drop alerts** — the moment your shields go down, an alert is pushed through
  [WxPusher](https://wxpusher.zjiecode.com), a third-party push service.
- **Log-silence alerts** — if the journal stops growing while you are AFK, the game has
  almost certainly disconnected. You get told, instead of finding out hours later.
- **Plus a web panel** — reachable from your phone or another PC on the same LAN: live
  status, kill-trend chart, bounty log and the raw event stream.

<img src="docs/win32-en.png" alt="Win32 interface" width="720">

## Features

| | |
|---|---|
| **Statistics** | Total kills and bounty, "last N" windows, per-kill bounty log, mission reward breakdown, kill-trend chart (recent kill rate and the last hour, both in kills per hour), session totals |
| **Alerts** | Shield drop and log silence, pushed through [WxPusher](https://wxpusher.zjiecode.com) — a third-party push service, not an official WeChat API. Repeated silence alerts are capped so an overnight disconnect cannot spam you |
| **Ship info** | Current ship, name/ID, cargo, main and reserve fuel — recovered from older journals when the current one has no `Loadout` event yet |
| **Web panel** | Live status, trend chart (inline SVG), bounty log, event stream. Served over the LAN, gzip-compressed, no build step, no CDN |
| **Bilingual UI** | 中文 / English, picked from the system locale by default and overridable in the config. Translations live in editable TOML files — adding a language needs no recompilation |
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
2. Open `config.toml`. If you want push alerts, fill in your WxPusher credentials
   (details in [Configuration](#configuration)):

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

> **Keep `config.toml` private.** It holds a credential that can push messages on your
> behalf. It is git-ignored here for that reason.

## Configuration

Every setting lives in `config.toml` — a plain TOML file **next to the executable**:

| Interface | How to open it |
|---|---|
| Win32 | *File → Open config* (`Ctrl+C`) opens it in your default editor |
| Tk | the *Open config* button on the main window |
| Console | edit the file by hand in the folder you unzipped into |

There is no installer and no registry entry: the file is created from the built-in
defaults on first run, and **deleting it regenerates those defaults**.

Rules of thumb when editing:

- **Restart the program after saving.** The file is read once at startup; nothing is
  hot-reloaded.
- Keep the key names, change only what is right of the `=`: strings need double quotes
  (`language = "English"`), booleans are lowercase (`true` / `false`), integers are bare
  (`max_list_len = 200`).
- Durations are strings with a unit suffix: `"500ms"`, `"2s"`, `"5m"`, `"1h"`.
- A misspelled key is ignored as an unknown entry, but **a syntax error drops the whole
  file back to the defaults** and says so in the log box — if a change seems to have had
  no effect, read the log first.

A complete file looks like this (the copy on disk already carries these comments, so
just edit the values):

```toml
# Elite Dangerous Journal Monitor - configuration
#
# Restart the program after editing. / 修改后重启程序生效。
# Durations accept 30s / 5m / 1h. / 时间字段支持 30s / 5m / 1h 这类写法。
# timezone: UTC+8 (default) | UTC | auto | UTC+5:30   (see tz.go)
# enable_panel = false never opens a port; monitoring and push still run.
#   为 false 时完全不监听端口，只运行监控与推送。
# wxpusher: alerts go out through WxPusher (wxpusher.zjiecode.com), a
#   third-party push service - not an official WeChat API. Fill in app_token
#   and uid to enable alerts; leave both empty to monitor without push.
#   推送走第三方服务 WxPusher（非微信官方接口）；填 app_token + uid 才会推送，
#   两者留空则只监控。凭据只留在本机，请勿外传。
# language: auto (default, follows the system) | 中文 | English
#   默认 auto：按系统区域自动切换界面语言；重启生效 (restart to apply)
#
# Delete this file to regenerate it from the built-in defaults.
# 删掉本文件会按内置默认值重新生成一份。
listen_addr = ":8088"
timezone = "UTC+8"
enable_panel = true
language = "auto"
poll_interval = "2s"
stall_threshold = "10m"
stat_window = "1h"
max_list_len = 200
history_scan_count = 5
toolbar_edge = "bottom"

[wxpusher]
  url = "https://wxpusher.zjiecode.com/api/send/message"
  app_token = ""
  uid = ""
```

### Common edits

**Switch the UI language** — the default is `language = "auto"`, which follows the
system locale (`zh-CN` → Chinese, `en-US` → English; any language whose
`lang/<code>.toml` claims that locale wins). Pin it with `"English"` / `"中文"` /
`"en"` if auto guesses wrong — the startup log reports what auto picked.

**Keep the panel off the LAN** — `listen_addr = "127.0.0.1:8088"`.

**Turn the web panel off entirely** — `enable_panel = false` never listens on a port;
monitoring and push keep running.

**Enable push alerts** — sign up at [WxPusher](https://wxpusher.zjiecode.com), copy your
app token and user UID, and fill in the `[wxpusher]` table:

```toml
[wxpusher]
  url = "https://wxpusher.zjiecode.com/api/send/message"   # leave as is
  app_token = "AT_..."                                     # your app token
  uid = "UID_..."                                          # your user UID
```

Both values must be present or push stays off. Startup logs which state you are in
(`WxPusher: enabled` or `WxPusher push: app token / user UID not set, push disabled`);
the Win32 build also shows a `WxPusher` line on the *Status* tab.

**Warn later about a silent journal** — if `stall_threshold = "10m"` is too eager for a
long AFK session, raise it to `"30m"` or `"1h"`.

### Keys

| Key | Default | Meaning |
|---|---|---|
| `listen_addr` | `":8088"` | Panel listen address. Use `"127.0.0.1:8088"` to keep it off the LAN |
| `timezone` | `"UTC+8"` | Display timezone. Accepts `UTC+8`, `UTC`, `auto`, `UTC+5:30`, `北京时间` |
| `enable_panel` | `true` | `false` never opens a port — monitoring and push still run |
| `language` | `"auto"` | `auto` follows the system locale; `中文` / `English` (`zh` / `en`) pin it |
| `poll_interval` | `"2s"` | How often the journal is re-read |
| `stall_threshold` | `"10m"` | Push an alert once the journal has been silent this long |
| `stat_window` | `"1h"` | Window used for the "last N" statistics |
| `max_list_len` | `200` | Maximum records returned per API call |
| `history_scan_count` | `5` | Historical journals to scan when ship data missing |
| `toolbar_edge` | `"bottom"` | **Win32 UI only.** On minimize, dock a thin (centred, text-width, rounded) toolbar to the screen bottom/top edge; draggable anywhere, double-click restores the main window (tray icon kept); empty = original tray-only behaviour. The Tk UI ignores this and logs a note when it is set |
| `wxpusher.url` | WxPusher endpoint | Push service endpoint; rarely needs changing |
| `wxpusher.app_token` | — | Your WxPusher app token (empty = no push) |
| `wxpusher.uid` | — | Your WxPusher user UID (empty = no push) |

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
and translate the `[strings]` tables. A Japanese machine then picks it up on its own:/nauto matches the system locale against every language's `tag`, so shipping
`lang/ja.toml` is enough. Put `language = "ja"` in `config.toml` only to force it
somewhere else. Any id you leave out falls back to the base language, so a
half-finished translation still works.
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
tools/                dev-time helpers (make_icon.py)
build.sh              build / check / upx / clean
```

## Releases

Pushing a tag matching `v*` triggers CI (`windows-latest`, no compiler needed) to run
`./build.sh all` and attach all five artifacts, plus `SHA256SUMS.txt`, to the GitHub
Release.

## Notes

- **Unofficial.** Not affiliated with or endorsed by Frontier Developments. *Elite
  Dangerous* is a trademark of Frontier Developments plc.
- Push goes through [WxPusher](https://wxpusher.zjiecode.com), a third-party push
  provider — not an official WeChat API. How it reaches you is its call; typically a
  message from its WeChat service account.
- Journal timestamps are UTC; everything displayed goes through the configured timezone.

## License

[MIT](LICENSE)

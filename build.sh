#!/usr/bin/env bash
# elite_mon build script -- switches between Win32 GUI / Tk GUI / console via build tags.
#
# Layout:
#   src/       all Go sources + frontend assets static/ + visual style manifest app.manifest/app.rc
#              + src/icon/ (app icons: app.ico for Windows resources, icon*.png for the Tk build)
#              (.syso is a build artifact: generated on demand and removed when the build ends)
#   tools/     dev-time tooling (make_icon.py generates the icon resources)
#   Release/   output directory (5 binaries + the runtime config.toml)
#   build.sh   this script
#
# Artifacts (all under Release/):
#   elite_mon.exe              Windows console build
#   elite_mon_win32.exe        Windows native Win32 UI build
#   elite_mon_tk.exe           Windows Tk UI build
#   elite_mon_linux_amd64      Linux console build
#   elite_mon_tk_linux_amd64   Linux Tk UI build
#
# Usage:
#   ./build.sh              default GUI for this machine (Windows → Win32; Linux → Tk)
#   ./build.sh win32        native Win32 UI      → Release/elite_mon_win32.exe (Windows only)
#   ./build.sh tk           Tk UI                → Windows: elite_mon_tk.exe / Linux: elite_mon_tk_linux_amd64
#   ./build.sh console      native console build → Windows: elite_mon.exe / Linux: elite_mon_linux_amd64
#   ./build.sh linux        Linux console build (cross-compile)
#   ./build.sh tk-linux     Linux Tk UI build (cross-compile)
#   ./build.sh all          build everything above
#   ./build.sh check        gofmt + vet + test across console / Win32 / Tk
#   ./build.sh upx          compress the artifacts under Release/ (optional; needs upx on PATH)
#   ./build.sh clean        delete the binaries under Release/ (keeps config.toml)
#
# Build tags:
#   · `gui` tag: selects GUI instead of console.
#   · `tk`  tag: on Windows, switches from the default Win32 to Tk (`gui` already means Tk on Linux).
#
#   Windows default `gui` (no tk) -> native Win32 UI (src/win32.go + src/gui.go, plain SDK, no third-party libs, no CGO)
#   Windows `gui,tk`              -> Tk UI (src/tk.go, modernc.org/tk9.0, pure-Go Tcl/Tk 9.0, no CGO, no bundled DLLs)
#   Linux   `gui`                 -> Tk UI (cross-compilable straight from Windows)
#   Linux   no tags               -> console
#
# Every variant builds with CGO_ENABLED=0, so all five artifacts cross-compile from a plain
# Go toolchain — no gcc, no MSYS2. (The IUP UI was removed for exactly this reason: it was the
# only CGO user, which kept it out of CI and added a vendored 6.5 MB dependency.)
#
# The Tk build uses modernc.org/tk9.0: Tcl/Tk 9.0 transpiled to pure Go (no CGO). At runtime it unpacks the
# embedded Tcl/Tk shared libraries into the user cache dir (%LOCALAPPDATA%/modernc.org/ or XDG_CACHE_HOME).
# So the Tk build is a single exe with no bundled tcl86.dll/tk86.dll/tcllib/, and cross-compiles in one step.
#
# ---------------------------------------------------------------------------
# "Modern look" for Windows UIs + app icon = app.manifest + app.ico
# ---------------------------------------------------------------------------
# src/app.manifest declares the Microsoft.Windows.Common-Controls 6.0 dependency; rsrc compiles it
# together with src/icon/app.ico into src/rsrc_windows_amd64.syso, which go build links into the exe
# (Windows/amd64 only; ignored elsewhere).
#
# The manifest decides which comctl32 is loaded:
#     present in the exe -> process loads comctl32 v6.0 from WinSxS -> widgets drawn with the system theme (modern look)
#     absent             -> process binds comctl32 v5.82                -> tabs/scrollbars/buttons all look Win95
#
# The icon (RT_GROUP_ICON) is what backs the exe file icon, the window class icon and the tray icon.
# ⚠️ Both share one .syso, since an exe can only hold one .rsrc -- rsrc assigns resource IDs in order:
#    manifest = 1, icon group = 2 (RT_ICON starts at 3). resIDIconGroup in win32.go relies on this
#    convention; after building, the resource directory is parsed to assert both IDs.
#
# ⚠️ That .syso is a **build artifact**, not source: it must sit next to the .go files for go build to
# link it automatically, but leaving it in the source tree is messy. So this script generates it
# temporarily for Windows targets and a trap deletes it when the build ends (including on Ctrl-C).
# Therefore **do not run `go build ./src` directly** (that exe would have no manifest, no icon and a
# Win95 look) -- use ./build.sh.
# Manual generation: GOSUMDB=off go run github.com/akavel/rsrc@v0.10.2 -manifest src/app.manifest -ico src/icon/app.ico -arch amd64 -o src/rsrc_windows_amd64.syso
# (rsrc is pure Go, so no windres/binutils needed -- MSYS2's windres on this machine is broken.)
#
# Env vars override: GO (default go), ARCH (default amd64).
#
# Why no Makefile: the make on this machine's PATH is MSYS2's (/d/msys/usr/bin/make), and it strips
# TEMP / USERPROFILE from child processes, so go will not even start. See the environment patching below.

set -euo pipefail
cd "$(dirname "$0")"

# ---- Path constants -----------------------------------------------------------
PKG=./src          # Go package directory (single main package, tests alongside sources)
OUTDIR=Release     # artifact output directory

# ---- Environment patching -----------------------------------------------------
# MSYS2's bash / make drop TEMP / TMP / USERPROFILE / LOCALAPPDATA / APPDATA when spawning
# children (only a dozen vars survive). All four consequences are subtle, so restore them here:
#
#   · no %TEMP%          Go creates its temp dir under C:\Windows -> "Access is denied"
#   · no %LOCALAPPDATA%  GOCACHE cannot be located -> "build cache is required..."
#   · no %USERPROFILE%   trend_realdata_test finds no real Journal -> **silent skip**
#                        looks green but never ran (bitten by this before)
#   · no %APPDATA%       go cannot read GOPROXY from %APPDATA%\go\env and falls back to the
#                        default proxy.golang.org (unreachable here) -> module fetch times out
#
# The real user dir is recovered from the existing Go build cache path (C:\Users\x\AppData\Local\go-build).
# If that fails we fall back to the temp dir, and check warns loudly rather than pretending it verified.
if [ -z "${USERPROFILE:-}" ] || [ -z "${LOCALAPPDATA:-}" ]; then
	tmppath="$(ls -d /c/Users/*/AppData/Local/go-build 2>/dev/null | head -1 || true)"
	if [ -n "$tmppath" ]; then
		tmppath="$(cygpath -w "$tmppath")"
		: "${LOCALAPPDATA:=${tmppath%\\go-build}}"
		: "${USERPROFILE:=${LOCALAPPDATA%\\AppData\\Local}}"
		export LOCALAPPDATA USERPROFILE
		echo "提示：MSYS2 剥掉了环境变量，已从 Go 构建缓存反推 USERPROFILE=$USERPROFILE" >&2
	fi
fi

# go stores its module proxy settings in %APPDATA%\go\env (GOPROXY=https://goproxy.cn,direct here).
# Without APPDATA, go falls back to the default proxy.golang.org, so `go run xxx@v1.2.3` hangs and
# then reports "connection attempt failed". Restoring it is enough.
if [ -z "${APPDATA:-}" ] && [ -n "${USERPROFILE:-}" ]; then
	APPDATA="${USERPROFILE}\\AppData\\Roaming"
	export APPDATA
fi

if [ -z "${TEMP:-}" ]; then
	TEMP="$(cygpath -w /tmp 2>/dev/null || echo .)"
	TMP="$TEMP"
	export TEMP TMP
fi

# go build stages intermediate files under TMPDIR, and Git Bash's /tmp can be read-only in a sandbox.
# If the project has a local writable temp dir, point TMPDIR at it so the build has somewhere to write.
LOCAL_TMP="$(cd "$(dirname "$0")" && pwd)/.tmp_verify/build_tmp"
if [ -z "${TMPDIR:-}" ] && [ -d "$LOCAL_TMP" ]; then
	TMPDIR="$(cygpath -w "$LOCAL_TMP")"
	export TMPDIR
fi

if [ -z "${GOCACHE:-}" ]; then
	GOCACHE="$(cygpath -w "${LOCALAPPDATA:-/tmp}/go-build" 2>/dev/null || echo .)"
	export GOCACHE
fi

GO=${GO:-go}
ARCH=${ARCH:-amd64}

# ---- Version stamping ---------------------------------------------------------
# Two halves, deliberately split:
#   · the tag is injected here as main.version (-X);
#   · the commit hash, commit date and dirty flag are stamped by the toolchain
#     itself (-buildvcs=auto, on by default => debug.ReadBuildInfo -> vcs.*),
#     which version.go reads. See versionInfo() for the exact rendering.
# So a tag build reports "v1.0.0 (a1b2c3d, 2026-09-15, clean)", an untagged local
# build "a1b2c3d (2026-09-15, dirty)", and a build from a source tarball "dev".
# --always matters: without it, an untagged repo makes git describe fail outright.
VERSION=""
if command -v git >/dev/null 2>&1; then
	VERSION="$(git describe --tags --always 2>/dev/null || true)"
fi

LDFLAGS=(-trimpath -ldflags="-s -w -X main.version=$VERSION")
GUILDFLAGS=(-trimpath -ldflags="-s -w -H windowsgui -X main.version=$VERSION")

is_windows() { case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) return 0 ;; *) return 1 ;; esac; }

# build_out <artifact path> [go build args...]
#
# Writing over an artifact is blocked by a **running instance** -- Windows locks the executing exe,
# so the write gives Access is denied. But Windows does allow renaming a running exe, so step aside:
# build to a temp name, move the old file to xxx_old.exe (a rollback point), then swap the new one in.
build_out() {
	local out="$1"; shift
	local err
	if err=$("$GO" build "$@" -o "$out" "$PKG" 2>&1); then
		ls -l "$out"
		return 0
	fi
	case "$out" in
	*.exe) ;;
	*) echo "$err" >&2; return 1 ;;
	esac
	echo "    （$out 直接写失败，多半是实例正在运行；改用临时名编译后重命名）"
	"$GO" build "$@" -o "$out.new" "$PKG" || { echo "$err" >&2; return 1; }
	[ -f "$out" ] && mv -f "$out" "${out%.exe}_old.exe"
	mv -f "$out.new" "$out"
	ls -l "$out"
}

# The Tk build now uses modernc.org/tk9.0 (pure Go, no bundled DLLs), so bundle_tk_runtime is gone.

# ---- Visual style manifest + app icon (temp .syso, deleted when done) --------------
# Whether the Windows build "looks like Win95" hinges on this .syso: it compiles app.manifest into
# the exe as an RT_MANIFEST resource, which is what makes Windows load comctl32 v6 and draw widgets
# with the system theme.
# The same .syso also embeds src/icon/app.ico as RT_GROUP_ICON -- that backs the exe file icon, the
# window class icon and the tray icon (an exe holds only one .rsrc, so manifest and icon ship together).
# go build links the .syso automatically (**it must sit next to the .go files**, hence src/ only),
# but it is a build artifact -- generated before the build and removed by trap when the build ends
# (including on Ctrl-C), leaving nothing behind in the source tree.
MANIFEST=src/app.manifest
APP_ICON=src/icon/app.ico
MANIFEST_SYSO=src/rsrc_windows_amd64.syso
MANIFEST_SYSO_TMP=0 # 1 = this .syso was generated by this run and must be deleted on exit
RSRC_PKG=github.com/akavel/rsrc
RSRC_VERSION=0.10.2 # pinned: @latest needs a version list lookup, which fails on this machine

gen_manifest_syso() {
	is_windows || return 0
	# Reuse it if this build already generated it (./build.sh all builds both Windows targets in a row)
	if [ "${MANIFEST_SYSO_TMP:-0}" = 1 ] && [ -f "$MANIFEST_SYSO" ]; then
		return 0
	fi
	echo "==> 生成临时资源对象 $MANIFEST_SYSO（清单 + 图标，构建结束自动删除）"
	# GOSUMDB=off: sum.golang.org is unreachable here (Bad Gateway), and rsrc is just a small
	# local tool, so a pinned version plus no checksum verification is enough; once the module is
	# cached it never goes online again.
	#
	# If the icon file is missing, degrade to manifest-only: better a UI without an icon than
	# letting one optional file break the whole .syso generation (which would also lose the visual
	# styles -- a far bigger cost).
	if [ -f "$APP_ICON" ]; then
		GOSUMDB=off "$GO" run "$RSRC_PKG@v$RSRC_VERSION" -manifest "$MANIFEST" \
			-ico "$APP_ICON" -arch amd64 -o "$MANIFEST_SYSO" || true
	else
		echo "    警告：缺 $APP_ICON → 产物无程序图标（exe/窗口/托盘都是系统默认图标）。" >&2
		echo "          生成： python tools/make_icon.py" >&2
		GOSUMDB=off "$GO" run "$RSRC_PKG@v$RSRC_VERSION" -manifest "$MANIFEST" \
			-arch amd64 -o "$MANIFEST_SYSO" || true
	fi
	if [ ! -f "$MANIFEST_SYSO" ]; then
		echo "警告：生成 $MANIFEST_SYSO 失败（多半是取不到 rsrc）。" >&2
		echo "      本次 Windows 产物会缺视觉样式清单 → 界面退回经典外观，功能不受影响。" >&2
		echo "      手工生成： GOSUMDB=off $GO run $RSRC_PKG@v$RSRC_VERSION -manifest $MANIFEST -ico $APP_ICON -arch amd64 -o $MANIFEST_SYSO" >&2
		return 0
	fi
	MANIFEST_SYSO_TMP=1
}

# Always delete the temporary .syso when the build ends (normal exit / error / Ctrl-C), keeping the source tree clean.
cleanup_manifest_syso() {
	if [ "${MANIFEST_SYSO_TMP:-0}" = 1 ]; then
		rm -f "$MANIFEST_SYSO"
	fi
}
trap cleanup_manifest_syso EXIT INT TERM

# verify_win_resources <exe>: after building, confirm the manifest and icon really made it into the
# artifact (so a lost .syso is spotted immediately).
#
# The manifest is matched as ASCII text; the icon is matched by PNG header (app.ico's 128/256 entries
# are embedded PNGs and appear verbatim in .rsrc) -- neither src/static/ nor pure Windows artifacts
# contain PNG, so a hit proves the icon resource is present. The Tk build itself //go:embed's three
# icon PNGs, so this check means little for it; the manifest line is the real evidence. To confirm
# the icon itself, run .tmp_verify/icon/probe_icon.py.
verify_win_resources() {
	local exe="$1" bad=0
	[ -f "$exe" ] || return 0
	if grep -qa "Microsoft.Windows.Common-Controls" "$exe"; then
		echo "    ✓ 视觉样式清单已链入 $exe"
	else
		bad=1
		echo "    警告：$exe 里没有视觉样式清单 → 界面会是经典外观。" >&2
	fi
	if grep -qa $'\x89PNG' "$exe"; then
		echo "    ✓ 程序图标已链入 $exe"
	else
		bad=1
		echo "    警告：$exe 里没有图标资源 → exe 图标/窗口/托盘都会是系统默认图标。" >&2
	fi
	if [ "$bad" = 1 ]; then
		echo "          （可执行 ./build.sh 重新生成 $MANIFEST_SYSO）" >&2
	fi
}

# ---- Per-variant builds -------------------------------------------------------

build_win32() {
	if ! is_windows; then
		echo "错误：Win32 界面只有 Windows 能构建（本机是 $(uname -s)）。" >&2
		return 1
	fi
	echo "==> Win32 原生界面版 → $OUTDIR/elite_mon_win32.exe（-tags gui，零 CGO，跟随系统视觉样式）"
	mkdir -p "$OUTDIR"
	gen_manifest_syso
	CGO_ENABLED=0 build_out "$OUTDIR/elite_mon_win32.exe" "${GUILDFLAGS[@]}" -tags gui
	verify_win_resources "$OUTDIR/elite_mon_win32.exe"
}

build_tk() {
	if is_windows; then
		echo "==> Windows Tk 界面版 → $OUTDIR/elite_mon_tk.exe（-tags gui,tk，纯 Go，无随包 DLL）"
		mkdir -p "$OUTDIR"
		gen_manifest_syso
		CGO_ENABLED=0 build_out "$OUTDIR/elite_mon_tk.exe" "${GUILDFLAGS[@]}" -tags gui,tk
		verify_win_resources "$OUTDIR/elite_mon_tk.exe"
	else
		build_tk_linux
	fi
}

build_console() {
	if is_windows; then
		echo "==> Windows 控制台版 → $OUTDIR/elite_mon.exe"
		mkdir -p "$OUTDIR"
		# The console build carries resources too: the icon (the exe icon in Explorer) and the visual
		# style manifest (it used to carry neither, so its dialogs and message boxes looked classic)
		gen_manifest_syso
		CGO_ENABLED=0 build_out "$OUTDIR/elite_mon.exe" "${LDFLAGS[@]}"
		verify_win_resources "$OUTDIR/elite_mon.exe"
	else
		build_linux
	fi
}

build_linux() {
	echo "==> Linux 控制台版 → $OUTDIR/elite_mon_linux_$ARCH（交叉编译 linux/$ARCH）"
	mkdir -p "$OUTDIR"
	GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 build_out "$OUTDIR/elite_mon_linux_$ARCH" "${LDFLAGS[@]}"
}

build_tk_linux() {
	echo "==> Linux Tk 界面版 → $OUTDIR/elite_mon_tk_linux_$ARCH（交叉编译 linux/$ARCH，-tags gui）"
	mkdir -p "$OUTDIR"
	GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 build_out "$OUTDIR/elite_mon_tk_linux_$ARCH" "${LDFLAGS[@]}" -tags gui
}

# The IUP UI was removed (see the header comment): it was the only CGO variant, and the only thing
# that needed gcc/MSYS2 plus a 6.5 MB vendored fork. This stays as an explicit tombstone instead of
# falling through to the unknown-target usage dump, so an old invocation gets a reason, not a wall
# of text. Drop it once the removed target is no longer muscle memory.
iup_removed() {
	echo "IUP 界面已移除——它是唯一的 CGO 形态（要 gcc / MSYS2），还拖着 6.5 MB 的 vendored 依赖。" >&2
	echo "请改用：./build.sh win32（原生 Win32 界面）或 ./build.sh tk（Tk 界面）。" >&2
	return 1
}

# check exercises all three variants: the untagged build is the easiest path to leave untested
# (win32.go / tk.go are excluded entirely, so compile errors only surface once that variant is built).
check() {
	if [ -z "${USERPROFILE:-}" ]; then
		echo "警告：USERPROFILE 为空（嵌套 MSYS2 shell 把它剥掉了）——"
		echo "      trend_realdata_test 会静默 skip，那部分等于没验。"
		echo "      想要真验：USERPROFILE='C:\\Users\\你的用户名' ./build.sh check"
	fi
	echo "==> gofmt $PKG"; gofmt -l "$PKG"; echo "(以上为空即干净)"
	echo "==> vet 控制台形态"; "$GO" vet ./...
	echo "==> vet Win32 默认 GUI 形态"; "$GO" vet -tags gui ./...
	echo "==> vet Tk 形态"; "$GO" vet -tags gui,tk ./...
	echo "==> vet Tk 形态(Linux 交叉)"; GOOS=linux "$GO" vet -tags gui ./...
	echo "==> test 控制台形态"; "$GO" test ./...
	echo "==> test Win32 默认 GUI 形态"; "$GO" test -tags gui ./...
	echo "==> test Tk 形态"; "$GO" test -tags gui,tk ./...
	echo "全部通过"
}

do_upx() {
	if ! command -v upx >/dev/null 2>&1; then
		echo "未找到 upx，跳过压缩。" >&2
		return 0
	fi
	for f in "$OUTDIR"/*; do
		case "$f" in
		*.exe | *_amd64) ;; # only binaries; skip config.toml and friends
		*) continue ;;
		esac
		# upx errors on already-compressed files; under set -e that must not break the loop.
		upx -q "$f" || true
	done
	ls -l "$OUTDIR"
}

clean() {
	rm -f "$OUTDIR"/elite_mon.exe "$OUTDIR"/elite_mon_win32.exe "$OUTDIR"/elite_mon_tk.exe \
		"$OUTDIR"/elite_mon_linux_$ARCH "$OUTDIR"/elite_mon_tk_linux_$ARCH \
		"$OUTDIR"/*_old.exe "$OUTDIR"/*.new "$MANIFEST_SYSO" 2>/dev/null || true
	echo "已删除 $OUTDIR/ 下的二进制与临时资源对象（config.toml 保留）"
}

usage() {
	sed -n '/^# Usage:/,/^# Build tags:/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
}

case "${1:-}" in
"")         if is_windows; then build_win32; else build_tk; fi ;;
gui | win32) build_win32 ;;
tk)         build_tk ;;
console)    build_console ;;
linux)      build_linux ;;
tk-linux)   build_tk_linux ;;
iup)        iup_removed ;;
all)
	if is_windows; then build_win32; fi
	build_tk
	build_console
	build_linux
	build_tk_linux
	;;
check) check ;;
upx)   do_upx ;;
clean) clean ;;
*)
	usage
	exit 1
	;;
esac

#!/usr/bin/env python3
"""Migrate an old config.json to config.toml (one-off, dev-machine tool).

The program used to read config.json and repair it in place: legacy Chinese keys
were renamed on load, a UTF-8 BOM was stripped, missing fields were filled back
in. None of that lives in the binary any more - it reads config.toml and never
rewrites it - so this script does the conversion once, by hand, where a mistake
is visible instead of silently applied to a running config.

It accepts both the legacy Chinese keys and the current English ones, which is
also what makes it useful for a config.json written by an older build.

Usage:
    python tools/migrate_config.py                    # Release/config.json -> Release/config.toml
    python tools/migrate_config.py path/to/config.json
    python tools/migrate_config.py in.json -o out.toml
    python tools/migrate_config.py --stdout           # print, write nothing
    python tools/migrate_config.py --force            # overwrite an existing config.toml

The output is byte-identical to what the program itself writes for the
equivalent config: same header, same key order, same formatting. Keep it that
way - tools/../.tmp_verify and the manual diff described in the README notes are
what hold the two in sync.
"""

import argparse
import json
import os
import sys

# Header written above every generated config.toml. Must stay byte-identical to
# configHeader in src/elite_monitor.go.
HEADER = """\
# Elite Dangerous Journal Monitor - configuration
#
# Restart the program after editing. / 修改后重启程序生效。
# Durations accept 30s / 5m / 1h. / 时间字段支持 30s / 5m / 1h 这类写法。
# timezone: UTC+8 (default) | UTC | auto | UTC+5:30   (see tz.go)
# enable_panel = false never opens a port; monitoring and push still run.
#   为 false 时完全不监听端口，只运行监控与推送。
# wxpusher: third-party push service (wxpusher.zjiecode.com), delivered through
#   a WeChat service account. Credentials stay on this machine only.
# language: 中文 | English   (restart to apply / 重启生效)
#
# Delete this file to regenerate it from the built-in defaults.
# 删掉本文件会按内置默认值重新生成一份。
"""

# Defaults and key order mirror the Config struct / defaultConfig in
# src/elite_monitor.go. A key missing from the input gets its built-in default
# here, so the migrated file is complete and self-documenting rather than
# relying on the program to fill gaps at runtime.
TOP_ORDER = [
    "listen_addr",
    "timezone",
    "enable_panel",
    "language",
    "poll_interval",
    "stall_threshold",
    "stat_window",
    "max_list_len",
    "history_scan_count",
]
TOP_DEFAULTS = {
    "listen_addr": ":8088",
    "timezone": "UTC+8",
    "enable_panel": True,
    "language": "中文",
    "poll_interval": "2s",
    "stall_threshold": "10m",
    "stat_window": "1h",
    "max_list_len": 200,
    "history_scan_count": 5,
}

WX_ORDER = ["url", "app_token", "uid"]
WX_DEFAULTS = {
    "url": "https://wxpusher.zjiecode.com/api/send/message",
    "app_token": "",
    "uid": "",
}

# Expected JSON type per key, so a bad value is reported instead of being
# written into a file the program will then refuse to parse.
TOP_TYPES = {
    "listen_addr": str,
    "timezone": str,
    "enable_panel": bool,
    "language": str,
    "poll_interval": str,
    "stall_threshold": str,
    "stat_window": str,
    "max_list_len": int,
    "history_scan_count": int,
}

# Legacy Chinese keys -> current English keys.
LEGACY_TOP = {
    "监听地址": "listen_addr",
    "时区": "timezone",
    "启用网页面板": "enable_panel",
    "语言": "language",
    "扫描间隔": "poll_interval",
    "静默告警阈值": "stall_threshold",
    "统计窗口": "stat_window",
    "列表最大长度": "max_list_len",
    "历史回溯份数": "history_scan_count",
    "WxPusher": "wxpusher",
}
LEGACY_WX = {
    "接口地址": "url",
    "应用令牌": "app_token",
    "用户UID": "uid",
}

# The "comment"/"说明" array held the documentation that now lives in the file's
# comment header, so it is dropped rather than carried over.
OBSOLETE = {"comment", "说明"}


def toml_str(value: str) -> str:
    """A TOML basic string. Same escaping Go's toml encoder uses."""
    return json.dumps(value, ensure_ascii=False)


def toml_value(value) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, str):
        return toml_str(value)
    raise ValueError("unsupported value type %r" % type(value).__name__)


def load_json(path: str) -> dict:
    with open(path, "rb") as fh:
        raw = fh.read()
    # utf-8-sig tolerates the BOM a Windows editor may have added.
    try:
        data = json.loads(raw.decode("utf-8-sig"))
    except UnicodeDecodeError as err:
        raise SystemExit("ERROR: %s is not valid UTF-8: %s" % (path, err))
    except json.JSONDecodeError as err:
        raise SystemExit("ERROR: %s is not valid JSON: %s" % (path, err))
    if not isinstance(data, dict):
        raise SystemExit("ERROR: %s must contain a JSON object" % path)
    return data


def convert(src: dict):
    """Return (top_values, wx_values, renamed, defaulted, dropped, ignored)."""
    renamed, ignored = [], []

    # Normalise every legacy key to its English name first. An existing English
    # key always wins, matching how the old in-place migration behaved.
    flat = {}
    for key, value in src.items():
        if key in OBSOLETE:
            continue
        if key in LEGACY_TOP:
            new = LEGACY_TOP[key]
            renamed.append((key, new))
        else:
            new = key
        if new in flat:
            continue
        flat[new] = value

    wx_src = {}
    if "wxpusher" in flat:
        raw_wx = flat.pop("wxpusher")
        if not isinstance(raw_wx, dict):
            raise SystemExit("ERROR: wxpusher must be a JSON object")
        for key, value in raw_wx.items():
            if key in LEGACY_WX:
                new = LEGACY_WX[key]
                renamed.append((key, "wxpusher." + new))
            else:
                new = key
            if new not in wx_src:
                wx_src[new] = value

    dropped = sorted(set(src) & OBSOLETE)
    defaulted = []

    top = {}
    for key in TOP_ORDER:
        if key in flat:
            want = TOP_TYPES[key]
            value = flat.pop(key)
            if not isinstance(value, want) or (want is int and isinstance(value, bool)):
                raise SystemExit(
                    "ERROR: %s must be %s, got %r" % (key, want.__name__, value)
                )
            top[key] = value
        else:
            top[key] = TOP_DEFAULTS[key]
            defaulted.append(key)

    wx = {}
    for key in WX_ORDER:
        if key in wx_src:
            value = wx_src.pop(key)
            if not isinstance(value, str):
                raise SystemExit("ERROR: wxpusher.%s must be a string" % key)
            wx[key] = value
        else:
            wx[key] = WX_DEFAULTS[key]
            defaulted.append("wxpusher." + key)

    ignored = sorted(list(flat) + ["wxpusher." + k for k in wx_src])
    return top, wx, renamed, defaulted, dropped, ignored


def render(top: dict, wx: dict) -> str:
    lines = [HEADER]
    for key in TOP_ORDER:
        lines.append("%s = %s\n" % (key, toml_value(top[key])))
    lines.append("\n[wxpusher]\n")
    for key in WX_ORDER:
        lines.append("  %s = %s\n" % (key, toml_value(wx[key])))
    return "".join(lines)


def note(msg: str) -> None:
    """Human-readable progress goes to stderr, so --stdout stays pipe-clean."""
    print(msg, file=sys.stderr)


def default_out_path(src_path: str) -> str:
    return os.path.join(os.path.dirname(os.path.abspath(src_path)), "config.toml")


def repo_root() -> str:
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Convert an old config.json into config.toml.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("input", nargs="?",
                    help="config.json to migrate (default: Release/config.json)")
    ap.add_argument("-o", "--out", help="output path (default: config.toml beside the input)")
    ap.add_argument("--stdout", action="store_true",
                    help="print the result instead of writing a file")
    ap.add_argument("--force", action="store_true",
                    help="overwrite the output file if it already exists")
    args = ap.parse_args()

    src_path = args.input or os.path.join(repo_root(), "Release", "config.json")
    if not os.path.isfile(src_path):
        print("ERROR: %s not found" % src_path, file=sys.stderr)
        return 1

    out_path = args.out or default_out_path(src_path)
    if not args.stdout and os.path.exists(out_path) and not args.force:
        print("ERROR: %s already exists (use --force to overwrite)" % out_path,
              file=sys.stderr)
        return 1

    source = load_json(src_path)
    top, wx, renamed, defaulted, dropped, ignored = convert(source)
    text = render(top, wx)

    note("read  %s (%d keys)" % (src_path, len(source)))
    if renamed:
        note("  renamed %d legacy Chinese key(s):" % len(renamed))
        for old, new in renamed:
            note("    %s -> %s" % (old, new))
    if dropped:
        note("  dropped %s (documentation now lives in the file's comment header)"
             % ", ".join(dropped))
    if defaulted:
        note("  filled %d key(s) that were absent, using the built-in default: %s"
             % (len(defaulted), ", ".join(defaulted)))
    if ignored:
        note("  IGNORED unknown key(s) the program does not read: %s"
             % ", ".join(ignored))
    if wx["app_token"] and wx["uid"]:
        note("  push credentials carried over: yes")
    else:
        note("  push credentials carried over: NO (app_token/uid empty)")

    if args.stdout:
        # Write bytes: Python's text-mode stdout on Windows rewrites \n as \r\n,
        # which would make the output differ from the file the program writes.
        sys.stdout.buffer.write(text.encode("utf-8"))
        sys.stdout.buffer.flush()
        return 0

    with open(out_path, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(text)
    note("wrote %s" % out_path)
    note("")
    note("The old %s was left untouched. The program no longer reads it, so once"
         % os.path.basename(src_path))
    note("you have confirmed the new config.toml works, you can delete it yourself.")
    return 0


if __name__ == "__main__":
    sys.exit(main())

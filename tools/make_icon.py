# -*- coding: utf-8 -*-
"""elite_mon 图标生成器（纯 Python 标准库，不需要 PIL / numpy / ImageMagick）。

图标用「256x256 设计坐标系里的矢量图元」描述，同一份图元同时驱动三件事：
  · render(size)  -> RGBA 栅格（用 SDF 解析覆盖率做抗锯齿，不靠超采样）
  · emit_svg()    -> 预览用 SVG（坐标同源，预览与成品一致）
  · .ico 打包      -> 小尺寸 DIB、大尺寸内嵌 PNG（Windows 对两者兼容性最好）

产出（默认写入 src/icon/）：
  app.ico        Windows 图标资源，由 rsrc 随清单一起编进 .syso →
                 exe 图标 / 窗口类图标 / 托盘图标都来自它
  icon256.png    给 Tk 版 wm iconphoto 用（//go:embed 进二进制）
  icon48.png     同上，多尺寸是为了让 Windows 按尺寸挑，别让它缩放 256 的
  icon16.png     同上

用法：
  python tools/make_icon.py                 # 生成 src/icon/ 下的资源文件
  python tools/make_icon.py --preview DIR   # 额外在 DIR 里出候选对照卡（挑款式用）

改设计就改下面的 d_radar() / d_hex() / d_monitor()，再把 DESIGN 指过去重跑。
"""

import argparse
import math
import os
import struct
import sys
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
ICON_DIR = os.path.join(ROOT, 'src', 'icon')

DESIGN = 'a_radar'   # 选中的款式：a_radar（雷达扫描盘）
UNIT = 256.0         # 设计画布边长

# ---- 配色：Elite Dangerous 的橙 + 深色底 ----
OR_HI = (255, 163, 54, 255)
OR_LO = (255, 102, 0, 255)
BADGE_HI = (30, 40, 60, 255)
BADGE_LO = (12, 17, 28, 255)
SCREEN = (16, 22, 34, 255)


# ==================================================================
# SDF（有符号距离，设计单位）
# ==================================================================
def _sdf_rr(px, py, x0, y0, x1, y1, r):
    """圆角矩形。"""
    cx, cy = (x0 + x1) / 2.0, (y0 + y1) / 2.0
    hx, hy = (x1 - x0) / 2.0 - r, (y1 - y0) / 2.0 - r
    qx, qy = abs(px - cx) - hx, abs(py - cy) - hy
    return math.hypot(max(qx, 0.0), max(qy, 0.0)) + min(max(qx, qy), 0.0) - r


def _sdf_ring(px, py, cx, cy, rad, hw):
    return abs(math.hypot(px - cx, py - cy) - rad) - hw


def _sdf_circle(px, py, cx, cy, rad):
    return math.hypot(px - cx, py - cy) - rad


def _sdf_seg(px, py, ax, ay, bx, by, hw):
    vx, vy = bx - ax, by - ay
    wx, wy = px - ax, py - ay
    L2 = vx * vx + vy * vy
    t = 0.0 if L2 == 0 else max(0.0, min(1.0, (wx * vx + wy * vy) / L2))
    return math.hypot(wx - t * vx, wy - t * vy) - hw


def _sdf_poly(px, py, pts):
    inside = False
    dmin = 1e18
    n = len(pts)
    for i in range(n):
        ax, ay = pts[i]
        bx, by = pts[(i + 1) % n]
        vx, vy = bx - ax, by - ay
        wx, wy = px - ax, py - ay
        L2 = vx * vx + vy * vy
        t = 0.0 if L2 == 0 else max(0.0, min(1.0, (wx * vx + wy * vy) / L2))
        d = math.hypot(wx - t * vx, wy - t * vy)
        if d < dmin:
            dmin = d
        if (ay > py) != (by > py):
            if px < ax + (py - ay) * (bx - ax) / (by - ay):
                inside = not inside
    return -dmin if inside else dmin


# ==================================================================
# 图元构造（统一补出 sdf / bbox）
# ==================================================================
def rr(x0, y0, x1, y1, r, fill):
    return dict(k='rr', box=(x0, y0, x1, y1), r=r, fill=fill,
                sdf=lambda px, py, b=(x0, y0, x1, y1), r_=r: _sdf_rr(px, py, b[0], b[1], b[2], b[3], r_))


def rrring(x0, y0, x1, y1, r, hw, fill):
    return dict(k='rrring', box=(x0 - hw, y0 - hw, x1 + hw, y1 + hw), r=r, hw=hw, fill=fill,
                sdf=lambda px, py, b=(x0, y0, x1, y1), r_=r, h=hw: abs(_sdf_rr(px, py, b[0], b[1], b[2], b[3], r_)) - h)


def ring(cx, cy, rad, hw, fill):
    return dict(k='ring', c=(cx, cy), rad=rad, hw=hw, fill=fill,
                box=(cx - rad - hw, cy - rad - hw, cx + rad + hw, cy + rad + hw),
                sdf=lambda px, py, c=(cx, cy), r_=rad, h=hw: _sdf_ring(px, py, c[0], c[1], r_, h))


def circle(cx, cy, rad, fill):
    return dict(k='circle', c=(cx, cy), rad=rad, fill=fill,
                box=(cx - rad, cy - rad, cx + rad, cy + rad),
                sdf=lambda px, py, c=(cx, cy), r_=rad: _sdf_circle(px, py, c[0], c[1], r_))


def seg(ax, ay, bx, by, hw, fill):
    return dict(k='seg', a=(ax, ay), b=(bx, by), hw=hw, fill=fill,
                box=(min(ax, bx) - hw, min(ay, by) - hw, max(ax, bx) + hw, max(ay, by) + hw),
                sdf=lambda px, py, a=(ax, ay), b=(bx, by), h=hw: _sdf_seg(px, py, a[0], a[1], b[0], b[1], h))


def poly(pts, fill):
    xs = [p[0] for p in pts]
    ys = [p[1] for p in pts]
    return dict(k='poly', pts=pts, fill=fill,
                box=(min(xs) - 1, min(ys) - 1, max(xs) + 1, max(ys) + 1),
                sdf=lambda px, py, P=pts: _sdf_poly(px, py, P))


def wedge(cx, cy, rad, a0, a1, fill, steps=28):
    pts = [(cx, cy)]
    for i in range(steps + 1):
        a = math.radians(a0 + (a1 - a0) * i / steps)
        pts.append((cx + rad * math.cos(a), cy + rad * math.sin(a)))
    w = poly(pts, fill)
    w['k'] = 'wedge'
    return w


# ==================================================================
# 三个候选设计
# ==================================================================
def badge():
    """统一的深色圆角徽章底 + 一圈极淡的白描边（深色任务栏上也有轮廓）。"""
    return [
        rr(10, 10, 246, 246, 54, ('grad', BADGE_HI, BADGE_LO)),
        rrring(10, 10, 246, 246, 54, 1.6, (255, 255, 255, 26)),
    ]


def d_radar():
    """A：雷达扫描盘（同心圆环 + 十字准线 + 扫描扇区 + 舰船箭头）。"""
    s = badge()
    cx, cy = 128, 132
    s.append(ring(cx, cy, 84, 13, ('grad', OR_HI, OR_LO)))
    s.append(seg(cx - 68, cy, cx + 68, cy, 6, (255, 138, 30, 105)))
    s.append(seg(cx, cy - 68, cx, cy + 68, 6, (255, 138, 30, 105)))
    s.append(wedge(cx, cy, 74, -90, -34, (255, 138, 30, 78)))
    s.append(poly([(128, 100), (102, 158), (154, 158)], ('grad', OR_HI, OR_LO)))
    return s


def d_hex():
    """B：六边形准星（实心橙六边形 + 挖空的舰船箭头，小尺寸最清楚）。"""
    s = badge()
    cx, cy, R = 128, 128, 92
    hexpts = [(cx + R * math.cos(math.radians(a)), cy + R * math.sin(math.radians(a)))
              for a in (-90, -30, 30, 90, 150, 210)]
    s.append(poly(hexpts, ('grad', OR_HI, OR_LO)))
    s.append(poly([(128, 84), (98, 158), (158, 158)], SCREEN))
    return s


def d_monitor():
    """C：监视屏（屏幕里是舰船 + 数据条，带支架）。"""
    s = badge()
    s.append(rr(42, 38, 214, 176, 22, ('grad', OR_HI, OR_LO)))
    s.append(rr(56, 52, 200, 162, 12, SCREEN))
    s.append(poly([(128, 74), (103, 132), (153, 132)], ('grad', OR_HI, OR_LO)))
    s.append(seg(94, 148, 162, 148, 5, (255, 138, 30, 150)))
    s.append(seg(128, 176, 128, 206, 11, ('grad', OR_HI, OR_LO)))
    s.append(seg(88, 212, 168, 212, 13, ('grad', OR_HI, OR_LO)))
    return s


DESIGNS = {'a_radar': (d_radar, '雷达扫描盘'),
           'b_hex': (d_hex, '六边形准星'),
           'c_monitor': (d_monitor, '监视屏')}

ICO_SIZES = [16, 24, 32, 48, 64, 128, 256]   # .ico 里放的尺寸
PNG_SIZES = [256, 48, 16]                    # 给 Tk iconphoto 的独立 PNG


# ==================================================================
# 栅格化
# ==================================================================
def _color_at(fill, bbox, y):
    if isinstance(fill, tuple) and fill and fill[0] == 'grad':
        c0, c1 = fill[1], fill[2]
        y0, y1 = bbox[1], bbox[3]
        t = 0.0 if y1 <= y0 else (y - y0) / (y1 - y0)
        t = 0.0 if t < 0 else (1.0 if t > 1 else t)
        return (c0[0] + (c1[0] - c0[0]) * t,
                c0[1] + (c1[1] - c0[1]) * t,
                c0[2] + (c1[2] - c0[2]) * t,
                c0[3] + (c1[3] - c0[3]) * t)
    return fill


def render(size, shapes):
    """返回 size*size 的 RGBA8（自上而下）。SDF 解析覆盖率 → 1px 抗锯齿带。"""
    scale = size / UNIT
    upx = UNIT / size          # 一个输出像素等于多少设计单位
    acc = [0.0] * (size * size * 4)
    for sh in shapes:
        x0, y0, x1, y1 = sh['box']
        ix0 = max(0, int(x0 * scale) - 1)
        ix1 = min(size, int(x1 * scale) + 2)
        iy0 = max(0, int(y0 * scale) - 1)
        iy1 = min(size, int(y1 * scale) + 2)
        if ix1 <= ix0 or iy1 <= iy0:
            continue
        sdf = sh['sdf']
        for py in range(iy0, iy1):
            dy = (py + 0.5) / scale
            row = py * size * 4
            for px in range(ix0, ix1):
                dx = (px + 0.5) / scale
                cov = 0.5 - sdf(dx, dy) / upx
                if cov <= 0.0:
                    continue
                if cov > 1.0:
                    cov = 1.0
                src = _color_at(sh['fill'], sh['box'], dy)
                sa = (src[3] / 255.0) * cov
                if sa <= 0.0:
                    continue
                i = row + px * 4
                da = acc[i + 3]
                oa = sa + da * (1.0 - sa)
                if oa <= 0.0:
                    continue
                k = da * (1.0 - sa)
                acc[i] = (src[0] / 255.0 * sa + acc[i] * k) / oa
                acc[i + 1] = (src[1] / 255.0 * sa + acc[i + 1] * k) / oa
                acc[i + 2] = (src[2] / 255.0 * sa + acc[i + 2] * k) / oa
                acc[i + 3] = oa
    out = bytearray(size * size * 4)
    for i in range(size * size):
        a = acc[i * 4 + 3]
        out[i * 4 + 3] = int(round(a * 255))
        if a > 0.0:
            for c in range(3):
                v = acc[i * 4 + c]
                out[i * 4 + c] = int(round((0.0 if v < 0 else (1.0 if v > 1 else v)) * 255))
    return bytes(out)


def blit(dst, dw, dh, src, sw, sh, x0, y0, alpha=1.0):
    for y in range(sh):
        sy = y0 + y
        if sy < 0 or sy >= dh:
            continue
        for x in range(sw):
            sx = x0 + x
            if sx < 0 or sx >= dw:
                continue
            si = (y * sw + x) * 4
            sa = src[si + 3] / 255.0 * alpha
            if sa <= 0.0:
                continue
            di = (sy * dw + sx) * 4
            da = dst[di + 3] / 255.0
            oa = sa + da * (1.0 - sa)
            if oa <= 0.0:
                continue
            k = da * (1.0 - sa)
            for c in range(3):
                dst[di + c] = int(round((src[si + c] * sa + dst[di + c] * k) / oa))
            dst[di + 3] = int(round(oa * 255))


# ==================================================================
# 编码：PNG / ICO
# ==================================================================
def png_bytes(w, h, rgba):
    raw = bytearray()
    for y in range(h):
        raw.append(0)
        raw += rgba[y * w * 4:(y + 1) * w * 4]

    def chunk(tag, data):
        c = tag + data
        return struct.pack('>I', len(data)) + c + struct.pack('>I', zlib.crc32(c) & 0xffffffff)

    return (b'\x89PNG\r\n\x1a\n'
            + chunk(b'IHDR', struct.pack('>IIBBBBB', w, h, 8, 6, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(bytes(raw), 9))
            + chunk(b'IEND', b''))


def dib_bytes(w, h, rgba):
    """32bpp BGRA 自下而上 + 1bpp AND 掩码（ICO 里小尺寸用 DIB 兼容性最好）。"""
    hdr = struct.pack('<IiiHHIIiiII', 40, w, h * 2, 1, 32, 0, 0, 0, 0, 0, 0)
    px = bytearray()
    for y in range(h - 1, -1, -1):
        row = rgba[y * w * 4:(y + 1) * w * 4]
        for x in range(w):
            r, g, b, a = row[x * 4], row[x * 4 + 1], row[x * 4 + 2], row[x * 4 + 3]
            px += bytes((b, g, r, a))
    stride = ((w + 31) // 32) * 4
    mask = bytearray()
    for y in range(h - 1, -1, -1):
        bits = bytearray(stride)
        for x in range(w):
            if rgba[(y * w + x) * 4 + 3] < 128:
                bits[x // 8] |= 0x80 >> (x % 8)
        mask += bits
    return bytes(hdr) + bytes(px) + bytes(mask)


def ico_bytes(entries):
    n = len(entries)
    head = struct.pack('<HHH', 0, 1, n)
    off = 6 + 16 * n
    dirs = bytearray()
    for size, data in entries:
        s = 0 if size >= 256 else size
        dirs += struct.pack('<BBBBHHII', s, s, 0, 0, 1, 32, len(data), off)
        off += len(data)
    return head + bytes(dirs) + b''.join(d for _, d in entries)


# ==================================================================
# SVG（预览用，与栅格同源）
# ==================================================================
def _hexc(c):
    return '#%02x%02x%02x' % (int(round(c[0])), int(round(c[1])), int(round(c[2])))


def _n(v):
    s = ('%.1f' % v)
    return s[:-2] if s.endswith('.0') else s


def emit_svg(shapes, size=256):
    defs, body = [], []
    for i, sh in enumerate(shapes):
        f = sh['fill']
        if isinstance(f, tuple) and f and f[0] == 'grad':
            c0, c1 = f[1], f[2]
            gid = 'g%d' % i
            defs.append('<linearGradient id="%s" x1="0" y1="0" x2="0" y2="1">'
                        '<stop offset="0" stop-color="%s" stop-opacity="%.3g"/>'
                        '<stop offset="1" stop-color="%s" stop-opacity="%.3g"/>'
                        '</linearGradient>' % (gid, _hexc(c0), c0[3] / 255.0, _hexc(c1), c1[3] / 255.0))
            paint, op = 'url(#%s)' % gid, None
        else:
            paint, op = _hexc(f), f[3] / 255.0
        o = '' if op is None else ' fill-opacity="%.3g"' % op
        st = '' if op is None else ' stroke-opacity="%.3g"' % op
        k = sh['k']
        if k == 'rr':
            x0, y0, x1, y1 = sh['box']
            body.append('<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"%s/>'
                        % (_n(x0 + 1.6), _n(y0 + 1.6), _n(x1 - x0 - 3.2), _n(y1 - y0 - 3.2), _n(sh['r']), paint, o))
        elif k == 'rrring':
            x0, y0, x1, y1 = sh['box']
            body.append('<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="none" stroke="%s" stroke-width="%s"%s/>'
                        % (_n(x0 + sh['hw']), _n(y0 + sh['hw']), _n(x1 - x0 - 2 * sh['hw']), _n(y1 - y0 - 2 * sh['hw']),
                           _n(sh['r']), paint, _n(2 * sh['hw']), st))
        elif k == 'ring':
            cx, cy = sh['c']
            body.append('<circle cx="%s" cy="%s" r="%s" fill="none" stroke="%s" stroke-width="%s"%s/>'
                        % (_n(cx), _n(cy), _n(sh['rad']), paint, _n(2 * sh['hw']), st))
        elif k == 'circle':
            cx, cy = sh['c']
            body.append('<circle cx="%s" cy="%s" r="%s" fill="%s"%s/>'
                        % (_n(cx), _n(cy), _n(sh['rad']), paint, o))
        elif k == 'seg':
            ax, ay = sh['a']
            bx, by = sh['b']
            body.append('<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-width="%s" stroke-linecap="round"%s/>'
                        % (_n(ax), _n(ay), _n(bx), _n(by), paint, _n(2 * sh['hw']), st))
        else:  # poly / wedge
            pts = ' '.join('%s,%s' % (_n(p[0]), _n(p[1])) for p in sh['pts'])
            body.append('<polygon points="%s" fill="%s"%s/>' % (pts, paint, o))
    return ('<svg viewBox="0 0 256 256" width="%d" height="%d" xmlns="http://www.w3.org/2000/svg">'
            % (size, size) + ('<defs>%s</defs>' % ''.join(defs) if defs else '') + ''.join(body) + '</svg>')


# ==================================================================
# 预览对照卡（只在 --preview 时需要）
# ==================================================================
CARD_W, CARD_H = 720, 274
BIG = 178
STRIP = [64, 48, 32, 24, 16]


def make_card(cache):
    canvas = bytearray(CARD_W * CARD_H * 4)
    for y in range(CARD_H):
        for x in range(CARD_W):
            i = (y * CARD_W + x) * 4
            if x < 360:
                r, g, b = 242, 243, 245
            else:
                r, g, b = 19, 22, 29
            canvas[i], canvas[i + 1], canvas[i + 2], canvas[i + 3] = r, g, b, 255
    for x in range(358, 362):
        for y in range(CARD_H):
            i = (y * CARD_W + x) * 4
            canvas[i] = canvas[i + 1] = canvas[i + 2] = 90
            canvas[i + 3] = 255
    big = cache[BIG]
    blit(canvas, CARD_W, CARD_H, big, BIG, BIG, 32, 18)
    blit(canvas, CARD_W, CARD_H, big, BIG, BIG, 392, 18)
    for x_start in (32, 392):
        x = x_start
        for s in STRIP:
            blit(canvas, CARD_W, CARD_H, cache[s], s, s, x, 214 + (64 - s))
            x += s + 20
    return bytes(canvas)


def preview(outdir):
    os.makedirs(outdir, exist_ok=True)
    cards = []
    for name, (fn, cn) in DESIGNS.items():
        shapes = fn()
        cache = {s: render(s, shapes) for s in ICO_SIZES + [BIG]}
        with open(os.path.join(outdir, name + '.svg'), 'w', encoding='utf-8') as f:
            f.write(emit_svg(shapes))
        card = make_card(cache)
        with open(os.path.join(outdir, name + '_card.png'), 'wb') as f:
            f.write(png_bytes(CARD_W, CARD_H, card))
        cards.append(card)
        print('%-11s %-8s card 已生成' % (name, cn))
    sheet = bytearray(CARD_W * (CARD_H * 3 + 8) * 4)
    sy = 0
    for c in cards:
        blit(sheet, CARD_W, CARD_H * 3 + 8, c, CARD_W, CARD_H, 0, sy)
        sy += CARD_H + 4
    with open(os.path.join(outdir, 'sheet.png'), 'wb') as f:
        f.write(png_bytes(CARD_W, CARD_H * 3 + 8, bytes(sheet)))
    print('sheet.png 已生成 →', outdir)


# ==================================================================
def main():
    ap = argparse.ArgumentParser(description='生成 elite_mon 图标资源')
    ap.add_argument('--preview', metavar='DIR',
                    help='额外在 DIR 输出三个候选款的对照卡（挑款式用），不影响正式产物')
    ap.add_argument('--design', default=DESIGN, choices=sorted(DESIGNS),
                    help='要输出成正式资源的款式（默认 %s）' % DESIGN)
    args = ap.parse_args()

    fn, cn = DESIGNS[args.design]
    shapes = fn()
    cache = {s: render(s, shapes) for s in sorted(set(ICO_SIZES) | set(PNG_SIZES))}

    os.makedirs(ICON_DIR, exist_ok=True)

    entries = [(s, png_bytes(s, s, cache[s]) if s >= 128 else dib_bytes(s, s, cache[s]))
               for s in ICO_SIZES]
    ico = ico_bytes(entries)
    ico_path = os.path.join(ICON_DIR, 'app.ico')
    with open(ico_path, 'wb') as f:
        f.write(ico)

    written = ['app.ico（%d 尺寸：%s）' % (len(ICO_SIZES), '/'.join(str(s) for s in ICO_SIZES))]
    for s in PNG_SIZES:
        p = os.path.join(ICON_DIR, 'icon%d.png' % s)
        with open(p, 'wb') as f:
            f.write(png_bytes(s, s, cache[s]))
        written.append('icon%d.png（%d B）' % (s, os.path.getsize(p)))

    print('款式：%s（%s）' % (args.design, cn))
    for w in written:
        print('  src/icon/' + w)
    print('  app.ico 总大小 %d B' % len(ico))

    if args.preview:
        preview(args.preview)
    return 0


if __name__ == '__main__':
    sys.exit(main())

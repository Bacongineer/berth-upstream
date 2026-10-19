# Chart frame / axis probe (dev only). R-01's contract, measured: every chart's
# axis words are HTML at their own CSS size (>= 11px at 375), the phone gets a
# 160px shape (the text is never what got shrunk), no chart draws a <text>
# inside a scaled viewBox, and tap-to-pin tooltips still work at 375.
# Run it the way check.py runs:
#   python3 -m http.server 8777 --bind 127.0.0.1 -d internal/harbor/web &
#   python3 test/ui/probe-charts.py http://127.0.0.1:8777/
# Optional: PROBE_W / PROBE_H set the viewport (default 375x812).
import os
import sys
from playwright.sync_api import sync_playwright

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8777/"
VW = int(os.environ.get("PROBE_W", "375"))
VH = int(os.environ.get("PROBE_H", "812"))
MIN_AXIS_PX = 11.0
MIN_TIDE_PX = 160.0
PHONE_W = 700  # the band the two min-heights belong to
PHONE = VW <= PHONE_W
# a rect 0.05px under a limit still misses it, and float layout is that noisy
TOL = 0.05
# the harbor tab's charts, then the engines tab's
HARBOR_HOSTS = ["#tide", "#timeline", "#machine-charts"]
ENGINE_HOSTS = ["#pool-chart", "#throughput-decode", "#throughput-rpm", "#residency-canvas"]
res = []


def ok(name, cond, extra=""):
    res.append(("PASS" if cond else "FAIL", name, str(extra)))


def info(name, extra=""):
    res.append(("INFO", name, str(extra)))


# every axis word in one host: its text, its computed size, its rect
LABELS_JS = """(sel) => [...document.querySelectorAll(sel + ' .chart-axis span')].map(s => ({
  t: s.textContent, cls: s.className, fs: parseFloat(getComputedStyle(s).fontSize),
  left: s.getBoundingClientRect().left, right: s.getBoundingClientRect().right,
  vis: !!s.getClientRects().length }))"""
# R-01's root cause is a <text> inside a stretched viewBox; sparklines never had one
SHAPES_JS = """(sel) => { const host = document.querySelector(sel);
  return { texts: [...document.querySelectorAll(sel + ' svg text')].map(t => t.textContent),
    svgs: document.querySelectorAll(sel + ' svg').length,
    frames: document.querySelectorAll(sel + ' .chart-frame, ' + sel + '.chart-frame').length,
    h: Math.round(host.getBoundingClientRect().height) }; }"""

with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page(viewport={"width": VW, "height": VH}, has_touch=True)
    errs = []
    pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
    pg.on("pageerror", lambda e: errs.append(str(e)))
    pg.goto(BASE + "?demo", wait_until="networkidle")
    pg.wait_for_timeout(1500)
    # advanced mode: the rpm chart and the machine charts paint only in it
    pg.click("#mode-advanced")
    pg.wait_for_timeout(400)

    def probe(sel):
        host = pg.locator(sel).first
        # a panel with nothing to draw says so with an empty-note (or CSS's own
        # :empty rule) and has no axis to measure; that is not a chart defect
        if not host.count() or not pg.evaluate(
                "(sel) => !!document.querySelector(sel + ' svg')", sel):
            info(f"{sel}: no chart rendered in this fixture", "skipped")
            return None
        host.scroll_into_view_if_needed()
        pg.wait_for_timeout(150)
        shapes = pg.evaluate(SHAPES_JS, sel)
        labels = [l for l in pg.evaluate(LABELS_JS, sel) if l["vis"]]
        # the phone contract is 11 px for every axis word (R-01/R-08). Above
        # 700 the shipped sizes ride unchanged, and one of them is the timeline's
        # dim engine line at --fs-micro (10 px) - the model under it is 11 px
        def under_min(l):
            return l["fs"] < MIN_AXIS_PX - TOL and (PHONE or "tl-eng" not in l["cls"])
        small = [(l["t"], l["fs"]) for l in labels if under_min(l)]
        out = [(l["t"], round(l["left"], 1), round(l["right"], 1)) for l in labels
               if l["left"] < -TOL or l["right"] > VW + TOL]
        ok(f"{sel}: axis words render as HTML", len(labels) > 0, f"{len(labels)} labels")
        ok(f"{sel}: axis text >= {MIN_AXIS_PX}px at {VW}", not small, small[:4])
        ok(f"{sel}: axis words stay in the viewport", not out, out[:4])
        ok(f"{sel}: no <text> inside its svg", not shapes["texts"], shapes["texts"][:4])
        info(f"{sel}: frame {shapes['h']}px, sizes", sorted({l["fs"] for l in labels}))
        return shapes

    tide_geo = None
    for sel in HARBOR_HOSTS:
        shapes = probe(sel)
        if sel == "#tide":
            ok("#tide: exactly one svg", shapes["svgs"] == 1, shapes["svgs"])
            if PHONE:
                ok(f"#tide: frame is at least {MIN_TIDE_PX}px tall",
                   shapes["h"] >= MIN_TIDE_PX - TOL, f"{shapes['h']}px")
            tide_geo = shapes
    pg.click("#tab-engines")
    pg.wait_for_timeout(600)
    for sel in ENGINE_HOSTS:
        shapes = probe(sel)
        if sel == "#residency-canvas":
            ok("#residency-canvas: exactly one svg (the axis is HTML)", shapes["svgs"] == 1,
               shapes["svgs"])
            if PHONE:
                ok(f"#residency-canvas: the phone box is at least {MIN_TIDE_PX}px",
                   shapes["h"] >= MIN_TIDE_PX - TOL, f"{shapes['h']}px")

    # the tide still draws its bands into the height the phone gave it, and its
    # x axis sits under the plot instead of over the legend (the harbor tab must
    # be on screen: a hidden tab measures 0 and would pass any of this)
    pg.click("#tab-harbor")
    pg.wait_for_timeout(400)
    geo = pg.evaluate("""() => { const f = document.querySelector('#tide .chart-frame');
      const svg = f.querySelector('svg');
      const fb = f.getBoundingClientRect(), sb = svg.getBoundingClientRect();
      const xs = [...f.querySelectorAll('.chart-axis span')].filter(s =>
        s.className.includes('axis-x') && !s.className.includes('total'));
      const bottom = Math.max(...xs.map(s => s.getBoundingClientRect().bottom));
      return { frameH: Math.round(fb.height), svgH: Math.round(sb.height),
               labels: xs.length, below: Math.round(bottom - fb.bottom) }; }""")
    ok("#tide: the shape fills the frame",
       (not PHONE or geo["frameH"] >= MIN_TIDE_PX - TOL)
       and abs(geo["svgH"] - geo["frameH"]) <= 1, geo)
    ok("#tide: x labels sit above the frame's bottom edge", geo["below"] <= 1, geo)

    # tap-to-pin, inside the subtree check.py reads (.tide-chart .chart-tip)
    tide = pg.locator("#tide svg").first
    tide.scroll_into_view_if_needed()
    pg.wait_for_timeout(200)
    bb = tide.bounding_box()
    ok("#tide: svg has a box to tap", bb is not None and bb["width"] > 100, bb)
    if bb:
        pg.touchscreen.tap(bb["x"] + bb["width"] * 0.5, bb["y"] + bb["height"] * 0.5)
        pg.wait_for_timeout(300)
        tip = pg.locator(".tide-chart .chart-tip").first
        vis = tip.count() > 0 and tip.is_visible()
        ok("#tide: tap pins the tooltip", vis, tip.inner_text()[:80].replace("\n", " | ") if vis else "")
        ok("#tide: the tooltip stays inside the chart",
           pg.evaluate("() => !!document.querySelector('.tide-chart .chart-tip')"))
        pg.touchscreen.tap(6, 6)
        pg.wait_for_timeout(300)
        ok("#tide: tap away dismisses it", not (tip.count() > 0 and tip.is_visible()))

    # focus parity: the keyboard still gets the same tip (keep-list item)
    pg.evaluate("() => document.querySelector('#tide .chart-frame').focus()")
    pg.wait_for_timeout(300)
    ok("#tide: focus shows the tooltip", pg.locator(".tide-chart .chart-tip").first.is_visible())
    pg.keyboard.press("Escape")
    pg.wait_for_timeout(200)

    # sparklines are untouched: no frame, no overlay, no words
    spark = pg.evaluate("""() => { const s = document.querySelector('.kpi-spark svg, .chart-spark');
      if (!s) return null; const box = s.closest('.kpi') || s.parentElement;
      return { texts: box.querySelectorAll('text').length, axis: box.querySelectorAll('.chart-axis').length,
               frame: box.querySelectorAll('.chart-frame').length }; }""")
    ok("sparkline: still text-free and frame-free", spark is not None
       and spark["texts"] == 0 and spark["axis"] == 0 and spark["frame"] == 0, spark)

    # the page itself never got wider because of an overlay
    sw = pg.evaluate("() => document.documentElement.scrollWidth")
    ok(f"no page-level horizontal overflow at {VW}", sw <= VW + TOL, sw)
    ok("no JS errors", not errs, errs[:3])
    b.close()

for r in res:
    print(*r, sep="\t")
fails = [r for r in res if r[0] == "FAIL"]
print(f"probe-charts: {len([r for r in res if r[0] == 'PASS'])} PASS, {len(fails)} FAIL"
      f" at {VW}x{VH} (tide frame {tide_geo and tide_geo['h']}px)")
sys.exit(1 if fails else 0)

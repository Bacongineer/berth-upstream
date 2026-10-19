# Tap-target audit (dev only). R-05's contract: on the phone band every control a
# thumb can hit is at least 44x44. Walks both tabs in both densities and reports
# every control below the line. Same server as check.py:
#   python3 -m http.server 8777 --bind 127.0.0.1 -d internal/harbor/web &
#   python3 test/ui/probe-touch.py http://127.0.0.1:8777/
# Optional: PROBE_W / PROBE_H set the viewport (default 375x812).
import os
import sys
from playwright.sync_api import sync_playwright

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8777/"
VW = int(os.environ.get("PROBE_W", "375"))
VH = int(os.environ.get("PROBE_H", "812"))
MIN = 44.0
TOL = 0.05
# the odometer digits are the read-out, not a control (plan step 7)
SKIP = [".reel", ".digit", ".chart-frame", ".chart-tip"]
res = []


def ok(name, cond, extra=""):
    res.append(("PASS" if cond else "FAIL", name, str(extra)))


AUDIT = """() => {
  const sel = 'a[href], button, input, select, textarea, summary, [role="button"], [tabindex]:not([tabindex="-1"])';
  const skip = %s;
  const out = [];
  for (const el of document.querySelectorAll(sel)) {
    if (skip.some(s => el.closest(s))) continue;
    if (!el.getClientRects().length) continue;
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    out.push({ id: el.id || '', tag: el.tagName.toLowerCase(),
      cls: (el.className || '').toString().split(' ').filter(Boolean).join('.'),
      w: Math.round(r.width * 10) / 10, h: Math.round(r.height * 10) / 10,
      txt: (el.textContent || '').trim().slice(0, 14),
      small: r.width < %s - %s || r.height < %s - %s });
  }
  return out;
}""" % (repr(SKIP), MIN, TOL, MIN, TOL)


def audit(pg, where):
    items = pg.evaluate(AUDIT)
    small = [i for i in items if i["small"]]
    ok(f"{where}: every control clears {MIN:.0f}px", not small, small[:8])
    return items


with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page(viewport={"width": VW, "height": VH}, has_touch=True)
    errs = []
    pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
    pg.on("pageerror", lambda e: errs.append(str(e)))
    pg.goto(BASE + "?demo", wait_until="networkidle")
    pg.wait_for_timeout(1500)
    items = audit(pg, "card/basic")
    # advanced density adds the machine charts' pickers and the full tables
    adv = pg.locator("#mode-advanced")
    if adv.is_visible():
        adv.click()
    else:
        pg.click("#menu-btn")
        pg.wait_for_timeout(250)
        pg.locator("#sheet-mode-advanced").click()
    pg.wait_for_timeout(400)
    audit(pg, "card/advanced")
    pg.click("#tab-engines")
    pg.wait_for_timeout(700)
    audit(pg, "engines/advanced")
    pg.click("#tab-harbor")
    pg.wait_for_timeout(400)

    # the gaps the same rules have to produce (R-09). A wrapped picker is
    # measured on the segments that share a line; otherwise on its own gap.
    gaps = pg.evaluate("""() => {
      const gapsOf = (sel, kid) => { const g = document.querySelector(sel);
        const bs = [...g.querySelectorAll(kid)].filter(b => b.getClientRects().length);
        let min = parseFloat(getComputedStyle(g).columnGap) || 0;
        for (let i = 1; i < bs.length; i++) {
          const a = bs[i - 1].getBoundingClientRect(), b = bs[i].getBoundingClientRect();
          if (Math.abs(a.top - b.top) < 1) min = Math.min(min, Math.round((b.left - a.right) * 10) / 10);
        }
        return min; };
      const g = document.querySelector('#counter-range'), b0 = g.querySelector('.mode-btn');
      return { seg: gapsOf('#counter-range', '.mode-btn'), chip: gapsOf('.log-chips', '.chip'),
        groupBorder: getComputedStyle(g).borderTopWidth,
        ownBorder: parseFloat(getComputedStyle(b0).borderTopWidth),
        ownRadius: getComputedStyle(b0).borderRadius }; }""")
    ok("picker segments are separate chips with a visible gap",
       gaps["seg"] >= 4 - TOL and gaps["ownBorder"] > 0 and gaps["groupBorder"] == "0px", gaps)
    ok("log chips sit at least 8px apart", gaps["chip"] >= 8 - TOL, gaps)
    fs = pg.evaluate("() => parseFloat(getComputedStyle(document.querySelector('#log-search')).fontSize)")
    ok("the filter field would not zoom the page (16px)", fs >= 16.0, fs)
    ok("no JS errors", not errs, errs[:3])
    b.close()

for r in res:
    print(*r, sep="\t")
fails = [r for r in res if r[0] == "FAIL"]
print(f"probe-touch: {len([r for r in res if r[0] == 'PASS'])} PASS, {len(fails)} FAIL at {VW}x{VH}")
sys.exit(1 if fails else 0)

# Copyright 2026 Trevin Tindall (T-Crypt)
# SPDX-License-Identifier: Apache-2.0

# Phone chrome probe (dev only). R-02's contract, measured on the phone band:
# one 56px rail, the .tabs element fixed to the bottom edge with the active tab
# marked, the counter above the fold, the menu sheet openable and closable with
# focus where it started, and no page-level overflow. Above the phone band it
# checks the opposite: the wide header rides unchanged.
# Same server as check.py:
#   python3 -m http.server 8777 --bind 127.0.0.1 -d internal/harbor/web &
#   python3 test/ui/probe-phone.py http://127.0.0.1:8777/
# Optional: PROBE_W / PROBE_H set the viewport (default 375x812).
import os
import sys
from playwright.sync_api import sync_playwright

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8777/"
VW = int(os.environ.get("PROBE_W", "375"))
VH = int(os.environ.get("PROBE_H", "812"))
PHONE_W = 700
PHONE = VW <= PHONE_W
RAIL_MAX = 58.0
TOL = 0.05  # float layout is noisy; 43.99994 still misses a 44 target
res = []


def ok(name, cond, extra=""):
    res.append(("PASS" if cond else "FAIL", name, str(extra)))


def info(name, extra=""):
    res.append(("INFO", name, str(extra)))


def rects(pg, sel):
    return pg.evaluate("""(sel) => [...document.querySelectorAll(sel)].map(e => {
      const r = e.getBoundingClientRect();
      return { id: e.id || e.className, h: r.height, w: r.width,
        vis: !!e.getClientRects().length, text: e.textContent.trim() }; })""", sel)


def phone_checks(pg):
    # --- the top rail: one row, and it is still the only thing at the top ----
    hdr = pg.locator(".app-header").first.bounding_box()
    ok(f"rail is at most {RAIL_MAX}px tall", hdr is not None and hdr["height"] <= RAIL_MAX + TOL, hdr)
    ok("rail is stuck to the top", pg.evaluate(
        "() => { const s = getComputedStyle(document.querySelector('.app-header'));"
        " return s.position === 'sticky' && s.top === '0px'; }"))
    row = pg.evaluate("""() => { const h = document.querySelector('.app-header');
      const hb = h.getBoundingClientRect();
      const kids = [...h.children].filter(k => getComputedStyle(k).display !== 'none'
        && k.getBoundingClientRect().width > 0 && !k.classList.contains('tabs'));
      const mids = kids.map(k => { const r = k.getBoundingClientRect(); return r.top + r.height / 2; });
      return { band: Math.max(...mids) - Math.min(...mids), n: kids.length,
        inside: kids.every(k => { const r = k.getBoundingClientRect();
          return r.top >= hb.top - 1 && r.bottom <= hb.bottom + 1; }),
        names: kids.map(k => k.className.split(' ')[0] || k.id) }; }""")
    ok("rail is one row", row["band"] <= 2 and row["inside"] and row["n"] == 4, row)
    pills = pg.evaluate("""() => [...document.querySelectorAll('.header-status .conn')].map(w => ({
      text: w.textContent.trim(), si: !!w.querySelector('.si'),
      extra: [...w.querySelectorAll('.msg, .ago')].filter(e => e.getClientRects().length).length }))""")
    ok("both state pills ride the rail with their word", len(pills) == 2 and all(p["text"] for p in pills), pills)
    ok("pills are dot-compressed (no message, no age)", not any(p["extra"] for p in pills), pills)
    hidden = pg.evaluate("""() => ({ ver: document.querySelector('#node-version').getClientRects().length,
      upd: document.querySelector('#last-update').getClientRects().length,
      keys: document.querySelector('#keys-btn').getClientRects().length })""")
    ok("the wide header's text facts are hidden, not deleted",
       all(v == 0 for v in hidden.values())
       and all(pg.locator(sel).count() == 1 for sel in ["#node-version", "#last-update", "#keys-btn"]), hidden)
    mode = pg.evaluate("""() => [...document.querySelectorAll('.app-header .mode > .mode-btn')].map(b => ({
      on: b.getAttribute('aria-pressed'), vis: !!b.getClientRects().length }))""")
    ok("density segment shows one label, both stay pressed-aware",
       len(mode) == 2 and sum(1 for m in mode if m["vis"]) == 1
       and sum(1 for m in mode if m["on"] == "true") == 1
       and mode[[m["on"] for m in mode].index("true")]["vis"], mode)

    # --- the bottom nav: the same .tabs, its bottom edge on the viewport -----
    tabs = pg.locator(".tabs").first.bounding_box()
    ok("bottom nav sits on the bottom edge",
       tabs is not None and abs(tabs["y"] + tabs["height"] - VH) <= TOL, tabs)
    ok("bottom nav is fixed", pg.evaluate(
        "() => getComputedStyle(document.querySelector('.tabs')).position === 'fixed'"))
    tabrects = pg.evaluate("""() => [...document.querySelectorAll('.tabs .tab')].map(t => {
      const r = t.getBoundingClientRect();
      const dot = t.querySelector('.tab-si');
      return { id: t.id, h: r.height, w: r.width, cur: t.getAttribute('aria-current'),
        dot: dot ? getComputedStyle(dot).display : '', words: t.querySelector('.tab-label').innerText.trim() }; })""")
    ok("every tab clears the 44px thumb zone", all(t["h"] >= 44 - TOL for t in tabrects),
       [round(t["h"], 1) for t in tabrects])
    ok("tabs are equal width (one uniform scale)",
       len({round(t["w"]) for t in tabrects}) == 1, [round(t["w"], 1) for t in tabrects])
    ok("one tab carries aria-current and its dot",
       sum(1 for t in tabrects if t["cur"] == "page") == 1
       and all((t["dot"] != "none") == (t["cur"] == "page") for t in tabrects), tabrects)
    ok("the phone nav reads CARD / ENGINES / ABOUT",
       [t["words"] for t in tabrects] == ["CARD", "ENGINES", "ABOUT"],
       [t["words"] for t in tabrects])
    # the nav is out of flow, so the page has to keep its own clearance below it
    ok("page content clears the nav", pg.evaluate("""() => { const m = document.querySelector('#main');
      const pad = parseFloat(getComputedStyle(m).paddingBottom); return pad >= 56; }"""))

    # --- the counter is above the fold now (R-02's whole point) --------------
    top = pg.evaluate("() => Math.round(document.querySelector('#sec-counter').getBoundingClientRect().top)")
    ok("counter starts above the fold without scrolling", 0 < top < VH, top)
    # the rail's compressed density must not swallow the range pickers (R-06).
    # Only the groups on this tab are laid out; the 44px thumb zone for a
    # picker segment lands with step 7's control pass.
    ranges = pg.evaluate("""() => [...document.querySelectorAll('.range-group')]
      .filter(g => g.offsetParent !== null).map(g => ({ id: g.id, n: g.querySelectorAll('.mode-btn').length,
      shown: [...g.querySelectorAll('.mode-btn')].filter(x => x.getClientRects().length).length }))""")
    ok("range pickers still show every option", len(ranges) == 2 and all(r["n"] == r["shown"] for r in ranges), ranges)
    # anchor jumps clear the rail, not the wide header
    pad = pg.evaluate("() => getComputedStyle(document.querySelector('#sec-tide')).scrollMarginTop")
    ok("panels keep the rail's scroll margin", pad == "64px", pad)

    # --- the board is cards, not a silent scroller (R-04) --------------------
    board = pg.evaluate("""() => { const b = document.querySelector('#board');
      const row = b.querySelector('.board-row'); const foot = b.querySelector('.board-foot');
      const cells = [...row.querySelectorAll('.cell')];
      return { over: b.scrollWidth - b.clientWidth, rows: b.querySelectorAll('.board-row').length,
        wrapped: !!row.closest('.board-rows'), rowsBeforeFoot: !!(row.closest('.board-rows')
          && row.closest('.board-rows').compareDocumentPosition(foot) & Node.DOCUMENT_POSITION_FOLLOWING),
        footVisible: foot.getClientRects().length > 0 && foot.getAttribute('role') === 'status',
        headHidden: !document.querySelector('.board-head').getClientRects().length,
        rowH: Math.round(row.getBoundingClientRect().height),
        labels: cells.map(c => getComputedStyle(c, '::before').content.replace(/"/g, '')),
        status: { text: row.querySelector('[data-col="status"]').textContent.trim(),
          si: row.querySelector('[data-col="status"] .si')
            ? getComputedStyle(row.querySelector('[data-col="status"] .si')).display !== 'none' : false } }; }""")
    ok("board does not scroll sideways inside its panel", board["over"] <= 1, board["over"])
    ok("board rows are wrapped and the foot stays below them", board["wrapped"] and board["rowsBeforeFoot"], board)
    ok("board foot stays visible as the live region", board["footVisible"], board["footVisible"])
    ok("column head is hidden on the phone", board["headHidden"])
    ok("first row reads as a 2-line card", board["rowH"] >= 50, board["rowH"])
    ok("every cell carries its own label", len(board["labels"]) == 7
       and all(l in ["TICKET", "VESSEL", "BERTH", "CLASS", "HOLD", "STATUS", "TIME"] for l in board["labels"]),
       board["labels"])
    ok("the status word shows with its shape", board["status"]["si"] and board["status"]["text"], board["status"])
    cells = pg.evaluate("""() => [...document.querySelectorAll('#board .board-row')].flatMap(r =>
      [...r.querySelectorAll('.cell')].map(c => ({ col: c.dataset.col,
        h: Math.round(c.getBoundingClientRect().height), over: c.scrollWidth - c.clientWidth })))""")
    ok("no cell stacks its tiles or clips them", all(c["h"] <= 44 and c["over"] <= 1 for c in cells),
       [c for c in cells if c["h"] > 44 or c["over"] > 1][:4])

    # --- R-07: Esc clears a typed filter from anywhere ----------------------
    pg.fill("#log-search", "granted")
    pg.wait_for_timeout(400)
    shown = pg.locator("#log-wrap .log-row").count()
    pg.evaluate("() => document.activeElement.blur()")
    pg.wait_for_timeout(150)
    pg.keyboard.press("Escape")
    pg.wait_for_timeout(400)
    after = pg.locator("#log-wrap .log-row").count()
    ok("Esc from outside the field clears the typed filter",
       pg.input_value("#log-search") == "" and after > shown, f"{shown} -> {after}")

    # --- the log's 2-line phone row (R-03) ----------------------------------
    log = pg.evaluate("""() => { const rows = [...document.querySelectorAll('#log-wrap .log-row')];
      if (!rows.length) return null;
      const wide = rows.map(r => r.querySelector('.ld')).sort((a, b) =>
        b.textContent.length - a.textContent.length)[0];
      const one = rows[0];
      return { n: rows.length, ws: getComputedStyle(wide).whiteSpace,
        fits: wide.scrollWidth - wide.clientWidth, hasDetail: wide.textContent.length > 0,
        wrapped: wide.getBoundingClientRect().height > 1.5 * (parseFloat(getComputedStyle(wide).lineHeight)
          || 1.2 * parseFloat(getComputedStyle(wide).fontSize)),
        chars: wide.textContent.trim().length,
        detailTitle: wide.getAttribute('title') === (wide.firstChild ? wide.firstChild.textContent : ''),
        engineTitle: one.querySelector('.le').getAttribute('title') === one.querySelector('.le').textContent,
        rowH: Math.round(one.getBoundingClientRect().height) }; }""")
    ok("the detail wraps instead of ellipsising", log and log["ws"] == "normal" and log["fits"] <= 1, log)
    ok("a long detail takes a second line and shows whole", log and log["wrapped"], log)
    ok("engine and detail carry their full text on title", log and log["detailTitle"] and log["engineTitle"], log)

    # --- the menu sheet: open, read, Esc, focus back on the button -----------
    pg.evaluate("() => window.scrollTo(0, 0)")
    pg.click("#menu-btn")
    pg.wait_for_timeout(300)
    ok("menu opens", pg.locator("#rail-menu").evaluate("d => d.open"))
    sects = pg.evaluate("""() => [...document.querySelectorAll('#rail-menu .sheet-sect')]
      .map(h => h.textContent.trim())""")
    ok("sheet is grouped and labelled", len(sects) >= 3, sects)
    meta = pg.evaluate("""() => ['sheet-node', 'sheet-read', 'sheet-conn'].map(i =>
      document.getElementById(i).textContent.trim())""")
    ok("sheet states versions, last read and connection", all(meta), meta)
    items = rects(pg, "#rail-menu .sheet-item")
    ok("sheet items clear the thumb zone", all(i["h"] >= 44 - TOL for i in items),
       [round(i["h"], 1) for i in items])
    pressed = pg.evaluate("""() => [...document.querySelectorAll('#rail-menu [data-mode]')]
      .map(i => i.getAttribute('aria-pressed'))""")
    ok("sheet shows which density is on", pressed.count("true") == 1, pressed)
    # the density switch works from the sheet (the rail shows only the current one)
    pg.locator("#sheet-mode-advanced").click()
    pg.wait_for_timeout(400)
    ok("sheet switches the density", pg.get_attribute("#mode-advanced", "aria-pressed") == "true")
    ok("switching from the sheet closes it", not pg.locator("#rail-menu").evaluate("d => d.open"))
    pg.click("#menu-btn")
    pg.wait_for_timeout(250)
    pg.keyboard.press("Escape")
    pg.wait_for_timeout(250)
    ok("Esc closes the sheet", not pg.locator("#rail-menu").evaluate("d => d.open"))
    ok("focus returns to the menu button", pg.evaluate("() => document.activeElement.id") == "menu-btn",
       pg.evaluate("() => document.activeElement.id"))
    # retry is the same action as R, and it does not navigate
    pg.click("#menu-btn")
    pg.wait_for_timeout(250)
    pg.locator("#sheet-retry").click()
    pg.wait_for_timeout(300)
    ok("RETRY NOW closes the sheet and stays on the page",
       not pg.locator("#rail-menu").evaluate("d => d.open") and "#rail-menu" not in pg.url)
    pg.click("#menu-btn")
    pg.wait_for_timeout(200)
    pg.locator("#sheet-keys").click()
    pg.wait_for_timeout(300)
    ok("the shortcut list opens from the sheet", pg.locator("#shortcuts").evaluate("d => d.open"))
    pg.keyboard.press("Escape")
    pg.wait_for_timeout(200)


def wide_checks(pg):
    """above the phone band the header rides unchanged"""
    hdr = pg.locator(".app-header").first.bounding_box()
    ok("wide header is in flow at the top", hdr is not None and hdr["height"] >= 60, hdr)
    ok("bottom nav is not fixed", pg.evaluate(
        "() => getComputedStyle(document.querySelector('.tabs')).position !== 'fixed'"))
    ok("tabs stay in the header", pg.evaluate("""() => { const t = document.querySelector('.tabs')
        .getBoundingClientRect(), h = document.querySelector('.app-header').getBoundingClientRect();
      return t.top >= h.top - 1 && t.bottom <= h.bottom + 1; }"""))
    ok("wide tabs name the whole surfaces",
       [t["words"] for t in pg.evaluate("""() => [...document.querySelectorAll('.tabs .tab')].map(t =>
        ({ words: t.querySelector('.tab-label').innerText.trim() }))""")]
       == ["CARD", "ENGINES & REQUESTS", "ABOUT"])
    ok("no dot on the wide tabs", pg.evaluate(
        "() => [...document.querySelectorAll('.tab-si')].every(s => !s.getClientRects().length)"))
    ok("keys button and text facts show", pg.evaluate("""() => document.querySelector('#keys-btn')
        .getClientRects().length > 0 && document.querySelector('.last-update')
        .getClientRects().length > 0"""))
    ok("menu button is not rendered", not pg.evaluate("() => !!document.querySelector('#menu-btn').getClientRects().length"))
    ok("both density labels show", pg.evaluate("""() => [...document.querySelectorAll('.app-header .mode > .mode-btn')]
      .every(b => b.getClientRects().length > 0)"""))
    ok("sheet stays closed", not pg.locator("#rail-menu").evaluate("d => d.open"))
    # the phone variants must not follow the row up here
    board = pg.evaluate("""() => { const b = document.querySelector('#board');
      const row = b.querySelector('.board-row');
      return { head: b.querySelector('.board-head').getClientRects().length > 0,
        rowH: Math.round(row.getBoundingClientRect().height),
        cellH: Math.round(row.querySelector('.cell').getBoundingClientRect().height),
        label: getComputedStyle(row.querySelector('.cell'), '::before').content,
        scrolls: b.scrollWidth >= b.clientWidth }; }""")
    ok("wide board keeps its head and its one-line rows",
       board["head"] and board["rowH"] <= 45 and board["cellH"] <= 25 and board["label"] == "none", board)
    ok("wide log row stays one line", pg.evaluate(
        "() => getComputedStyle(document.querySelector('.log-row .ld')).whiteSpace === 'nowrap'"))


with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page(viewport={"width": VW, "height": VH}, has_touch=True)
    errs = []
    pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
    pg.on("pageerror", lambda e: errs.append(str(e)))
    pg.goto(BASE + "?demo", wait_until="networkidle")
    pg.wait_for_timeout(1500)
    if PHONE:
        phone_checks(pg)
    else:
        wide_checks(pg)
    sw = pg.evaluate("() => document.documentElement.scrollWidth")
    ok(f"no page-level horizontal overflow at {VW}", sw <= VW + TOL, sw)
    ok("no JS errors", not errs, errs[:3])
    b.close()

for r in res:
    print(*r, sep="\t")
fails = [r for r in res if r[0] == "FAIL"]
print(f"probe-phone: {len([r for r in res if r[0] == 'PASS'])} PASS, {len(fails)} FAIL at {VW}x{VH}")
sys.exit(1 if fails else 0)

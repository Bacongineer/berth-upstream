// The library: every model on this machine, with its shelf, quant, size, how
// much of it the kernel page cache holds (COOL/COLD), the engine serving it
// when one reports it resident, and that engine's estimated VRAM. Data is
// GET /v1/models (or the demo fixture); served-by and est. VRAM join from
// the node's status the page already polls.
import { el, fmtBytes } from "./util.js";
import { icon, toast, copyText, errorNote } from "./ui.js";

// A model is one thing under several names: an engine's id an OpenAI request
// used, an Ollama tag, the file's stem. Score a pair: 2 when the lowercased
// names are equal, 1 when either contains the other, 0 when they say nothing
// to each other. The longest name wins a tie, the more specific is the one
// that was meant, the same rule the usage match keeps.
function matchScore(engineName, modelName) {
  const a = String(engineName || "").toLowerCase().trim();
  const b = String(modelName || "").toLowerCase().trim();
  if (!a || !b) return 0;
  if (a === b) return 2;
  if (a.includes(b) || b.includes(a)) return 1;
  return 0;
}

export class LibraryView {
  constructor(root, meta, onRetry) {
    this.root = root;
    this.meta = meta;
    this.doc = null;
    this.sig = "";
    this.onRetry = onRetry || null;
  }
  // doc: a /v1/models payload; status: the node's latest, for served-by and
  // the engines' estimated VRAM. Only the parts that moved repaint.
  render(doc, status) {
    if (doc) this.doc = doc;
    if (!doc || !Array.isArray(doc.models)) return;
    const skel = this.root.querySelector(":scope > .skel-box");
    if (skel) skel.remove();
    const serving = servingNow(this.doc.models, status);
    const sig = JSON.stringify([this.doc.count, this.doc.companions, this.doc.cache_checked, this.doc.models, serving]);
    if (sig === this.sig) return;
    this.sig = sig;
    this.root.replaceChildren();
    if (this.doc.models.length === 0) {
      if (this.doc.status === "failed" && (this.doc.errors || []).length) {
        this.root.append(libErrors(this.doc.errors));
      } else {
        this.root.append(el("div", "empty-note",
          "no models in the configured roots; set [models] dirs in berth.toml or list with berth models"));
      }
      this.meta.textContent = "no models";
      return;
    }
    if (this.doc.status === "failed" && (this.doc.errors || []).length) {
      this.root.append(libErrors(this.doc.errors));
    }
    const rows = el("tbody");
    for (const m of this.doc.models) {
      rows.append(this.row(m, serving[m.name.toLowerCase()]));
    }
    const table = el("table", "ltable lib-table");
    const thead = el("thead");
    const hr = el("tr");
    for (const [label, cls] of [["MODEL", ""], ["SHELF", ""], ["QUANT", ""], ["SIZE", "num"],
      ["CACHED", "num"], ["SERVED BY", ""], ["EST VRAM", "num"], ["", "lib-cmd-h"]]) {
      const cell = el("th", cls);
      cell.textContent = label;
      hr.append(cell);
    }
    thead.append(hr);
    table.append(thead, rows);
    const wrap = el("div", "recent-wrap");
    wrap.append(table);
    const foot = el("div", "lib-foot");
    foot.textContent = libFoot(this.doc);
    this.root.append(wrap, foot);
    this.meta.textContent = this.doc.count + " model" + (this.doc.count === 1 ? "" : "s")
      + ", " + fmtBytes(this.doc.bytes) + " in " + this.doc.roots.length + " root"
      + (this.doc.roots.length === 1 ? "" : "s");
  }
  row(m, serve) {
    const tr = el("tr");
    if (m.err) {
      tr.className = "bad";
      const td = el("td", "", m.name || m.path || "unreadable");
      td.colSpan = 8;
      td.title = (m.err || "") + (m.path ? " (" + m.path + ")" : "");
      tr.append(td);
      return tr;
    }
    const name = el("td", "", m.name || "?");
    name.title = (m.path || "") + (m.title && m.title !== m.name ? " - " + m.title : "");
    const shelf = el("td", "", m.shelf || "-");
    const quant = el("td", "", quantText(m));
    const size = el("td", "num", fmtBytes(m.bytes));
    const cache = el("td", "num", cachedText(m, this.doc.cache_checked));
    const byCell = el("td", "", serve ? serve.engines.join(", ") : "-");
    const vramCell = el("td", "num", serve && serve.vrams.length
      ? (serve.vrams[0].text + (serve.vrams.length === 1 ? "" : " +" + (serve.vrams.length - 1)))
      : "-");
    if (serve) {
      byCell.title = "engines reporting " + (m.name || "this model") + " resident now";
      vramCell.title = "the engine's learned footprint from its past runs: an estimate of what a run needs, never a reservation";
    }
    const cmd = el("td", "lib-cmd");
    for (const [label, command] of [["prefetch", "berth prefetch " + m.name], ["show", "berth models show " + m.name]]) {
      const btn = el("button", "lib-cmd-btn");
      btn.type = "button";
      btn.setAttribute("aria-label", "copy: " + command);
      btn.title = command;
      btn.append(icon("copy", 11), el("span", null, label));
      btn.addEventListener("click", async () => {
        const ok = await copyText(command);
        toast(ok ? command + " copied" : "copy failed", ok ? "good" : "bad");
      });
      cmd.append(btn);
    }
    tr.append(name, shelf, quant, size, cache, byCell, vramCell, cmd);
    return tr;
  }
  // A failed ask replaces the shimmer with its own retry, once, before the
  // first listing arrives.
  failed() {
    if (this.doc) return;
    const skel = this.root.querySelector(":scope > .skel-box");
    if (!skel) return;
    skel.remove();
    this.root.replaceChildren(errorNote("the library could not be read", () => { if (this.onRetry) this.onRetry(); }));
  }
}

// quantText is the listing's QUANT cell: the file type, then the part count
// of a split model.
function quantText(m) {
  let q = m.quant || "-";
  if ((m.parts || 0) > 1) q += "/" + m.parts;
  return q;
}

// cachedText is the CACHED cell: the model's resident share in the kernel
// page cache with the tier it reads as, COOL at half the bytes or more,
// COLD below. A dash when the check was skipped or measured nothing.
function cachedText(m, checked) {
  if (!checked || !m.cached_size) return "-";
  const share = Math.round((100 * (m.cached_bytes || 0)) / m.cached_size);
  return share + "% " + (share >= 50 ? "COOL" : "COLD");
}

// servingNow ties the engine table to the library: which engines report a
// model resident, and each engine's learned footprint. Returns lowercased
// library name -> { engines, vrams }.
function servingNow(models, status) {
  const fp = {};
  for (const f of (status && status.footprints) || []) {
    if (f.engine && f.estimated_vram_bytes > 0) {
      fp[f.engine] = { text: (f.estimated_vram_bytes / 1073741824).toFixed(1) + " GiB", runs: f.runs };
    }
  }
  const out = {};
  for (const eng of (status && Array.isArray(status.engines)) ? status.engines : []) {
    for (const m of eng.models || []) {
      if (!m.name) continue;
      let best = null;
      let score = 0;
      for (const doc of models) {
        const s = matchScore(m.name, doc.name);
        if (s === 0) continue;
        if (s > score || (s === score && best !== null && doc.name.length > best.length)) { best = doc.name.toLowerCase(); score = s; }
      }
      if (!best) continue;
      const e = out[best] || (out[best] = { engines: [], vrams: [] });
      if (!e.engines.includes(eng.name || "?")) e.engines.push(eng.name || "?");
      const f = fp[eng.name];
      if (f && !e.vrams.some((v) => v.text === f.text)) e.vrams.push(f);
    }
  }
  return out;
}

// libFoot is the totals line under the listing: the whole library's files,
// companions counted but not listed.
function libFoot(doc) {
  const n = doc.files - doc.companions;
  if (!(doc.companions > 0)) return n + " files in " + doc.roots.length + " root" + (doc.roots.length === 1 ? "" : "s");
  return n + (n === 1 ? " row" : " rows") + " +" + doc.companions
    + " companion file" + (doc.companions === 1 ? "" : "s") + " (vision projector, draft) - \"berth models --all\" lists them";
}

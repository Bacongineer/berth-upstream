// Small shared helpers: formatting and safe DOM building.
// Nothing here touches the API; API strings only ever reach textContent.
export const GIB = 1073741824;
export const MIB = 1048576;
export function $(id) {
  return document.getElementById(id);
}
// el("div", "a b", "text") builds a node; children may be strings or nodes.
export function el(tag, cls, ...children) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  for (const c of children) {
    if (c == null) continue;
    n.append(c instanceof Node ? c : String(c));
  }
  return n;
}
export function fmtGiB(bytes, dp = 1) {
  if (!Number.isFinite(bytes)) return "-";
  return (bytes / GIB).toFixed(dp);
}
export function fmtMiB(bytes, dp = 0) {
  if (!Number.isFinite(bytes) || bytes === 0) return "";
  return (bytes / MIB).toFixed(dp) + " MiB";
}
// Ages are seconds at the wire; human form, no sub-minute noise.
export function fmtAge(sec) {
  if (sec == null || !Number.isFinite(sec)) return "";
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m " + (s % 60) + "s";
  const h = Math.floor(m / 60);
  if (h < 48) return h + "h " + (m % 60) + "m";
  return Math.floor(h / 24) + "d " + (h % 24) + "h";
}
export function fmtClock(d) {
  const p = (x) => String(x).padStart(2, "0");
  return p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
}
export function fmtDate(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  const p = (x) => String(x).padStart(2, "0");
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate());
}
export function pad(n, width) {
  return String(n).padStart(width, "0");
}
export function fmtNum(n) {
  if (n == null || !Number.isFinite(n)) return "";
  if (Math.abs(n) >= 1000000) return (n / 1000000).toFixed(2) + "M";
  if (Math.abs(n) >= 10000) return (n / 1000).toFixed(1) + "k";
  return String(Math.round(n));
}
// The berth label comes from the scheduled GPUs the status reports. Past
// three cards a joined list stops fitting, so it gives the count.
export function berthLabel(status) {
  if (!status || !Array.isArray(status.gpus)) return "";
  const sched = status.gpus.filter((g) => g.scheduled);
  if (sched.length === 0) return "no card scheduled";
  if (sched.length > 3) return sched.length + " cards";
  return sched.map((g) => "gpu" + g.index).join("+");
}
// cardTag names the card a lease, waiting row or squatter sits on, but only
// when the node schedules more than one: a single card needs no tag.
export function cardTag(status, card) {
  const n = ((status && status.gpus) || []).filter((g) => g.scheduled).length;
  return n > 1 && card ? card : "";
}
// leaseGpu is the GPU a lease sits on. A lease with no card (an older berthd)
// gets the first scheduled card.
export function leaseGpu(status, card) {
  const gpus = (status && status.gpus) || [];
  return (card && gpus.find((g) => g.card === card)) || gpus.find((g) => g.scheduled) || null;
}
// cardRange names a lease's cards: "gpu0..gpu4" when they are consecutive,
// else a comma list in index order.
export function cardRange(cards) {
  const idx = cards.map((c) => /^gpu(\d+)$/.exec(c));
  if (idx.length < 2 || idx.some((m) => !m)) return cards.join(",");
  const n = idx.map((m) => Number(m[1])).sort((a, b) => a - b);
  const sorted = n.map((i) => "gpu" + i);
  return n.every((v, i) => i === 0 || v === n[i - 1] + 1) ? sorted[0] + ".." + sorted[n.length - 1] : sorted.join(",");
}
// leaseCardLine is what a lease serves: the models its engine reports loaded
// or ready, or the lease's engine name when no engine of that name reports
// one (a llama-swap model's lease is named for the model already). cards
// lists the lease's cards when it sits on more than one (a lease on several
// cards is one share per card, one id).
export function leaseCardLine(status, lease) {
  const eng = ((status && status.engines) || []).find((e) => e.name === lease.engine);
  const up = ((eng && eng.models) || []).filter((m) => m.state === "loaded" || m.state === "ready").map((m) => m.name);
  const model = up.length > 0 ? up.join(", ") : lease.engine;
  const cards = lease.id
    ? ((status && status.leases) || []).filter((l) => l.id === lease.id && l.card).map((l) => l.card)
    : [];
  return { model, cards: cards.length > 1 ? cardRange(cards) : "" };
}
// tideCapacity is what the memory history is drawn against: the scheduled
// cards' totals and baselines in bytes, summed, because the holders are
// summed over every card. title is "gpu0" for one card, "N cards" for more.
export function tideCapacity(status) {
  const sched = ((status && status.gpus) || []).filter((g) => g.scheduled);
  if (sched.length === 0) return null;
  return {
    total: sched.reduce((a, g) => a + (g.total || 0), 0),
    baseline: sched.reduce((a, g) => a + (g.baseline || 0), 0),
    title: sched.length === 1 ? "gpu" + sched[0].index : sched.length + " cards",
  };
}
// proto Priority -> ticket class
export function classWord(priority) {
  switch (priority) {
    case "foreground": return "FIRST";
    case "interactive": return "CABIN";
    case "batch": return "STEERAGE";
    default: return (priority || "UNSORTED").toUpperCase();
  }
}
// proto Lease.State -> tile tone (words live in board.js)
export function stateTone(state) {
  switch (state) {
    case "waiting": return "amber";
    case "active": return "cyan";
    case "draining": return "amber";
    case "parking": return "amber";
    case "parked": return "green";
    case "releasing": return "dim";
    case "stuck": return "magenta";
    default: return "dim";
  }
}
// proto Event.Kind -> short word on the harbor log
export function kindWord(kind) {
  switch (kind) {
    case "lease.requested": return "REQUESTED";
    case "lease.waiting": return "WAITING";
    case "lease.granted": return "GRANTED";
    case "lease.draining": return "DRAINING";
    case "lease.released": return "RELEASED";
    case "lease.orphaned": return "ORPHANED";
    case "lease.freed": return "FREED";
    case "lease.stuck": return "AGROUND";
    case "lease.refused": return "REFUSED";
    case "lease.reattached": return "REBOARDED";
    case "lease.parked": return "PARKED";
    case "lease.resumed": return "RESUMED";
    case "lease.unparked": return "UNPARKED";
    case "lease.park_refused": return "PARK FAILED";
    case "lease.park_evicted": return "EVICTED";
    case "lease.lending": return "LENDING";
    case "lease.lent": return "LENT";
    case "lease.returned": return "RETURNED";
    case "lease.lend_failed": return "LEND FAILED";
    case "lease.juggle": return "JUGGLE";
    case "squatter.appeared": return "STOWAWAY";
    case "squatter.gone": return "STOWAWAY GONE";
    case "probe.failed": return "PROBE DOWN";
    case "probe.recovered": return "PROBE UP";
    case "alert.raised": return "ALERT";
    case "alert.cleared": return "ALERT CLEAR";
    case "card.passthrough": return "GPU TO VM";
    case "card.returned": return "GPU BACK";
    default: return String(kind || "EVENT").toUpperCase();
  }
}
export function kindGroup(kind) {
  if (kind.startsWith("lease.")) return "leases";
  if (kind.startsWith("squatter.")) return "stowaways";
  if (kind.startsWith("probe.")) return "probe";
  if (kind.startsWith("alert.")) return "alerts";
  return "leases";
}
export function kindCss(kind) {
  switch (kind) {
    case "lease.granted":
    case "lease.reattached":
      return "kind-granted";
    case "lease.stuck": return "kind-stuck";
    case "lease.refused": return "kind-refused";
    case "squatter.appeared": return "kind-stowaway";
    case "probe.failed": return "kind-down";
    case "probe.recovered": return "kind-probe-up";
    case "lease.waiting": return "kind-waiting";
    case "lease.draining": return "kind-draining";
    case "alert.raised":
    case "alert.cleared":
      return "kind-alert";
    default: return "";
  }
}
// ---------------- the status indicator ----------------
// One vocabulary for every state word (spec 2.7): a shape that carries the
// meaning, then the word itself. dot = working now, ring = waiting or passive,
// half = in transition, triangle = needs attention, square = not running or
// not live. The five lease states are board.js's five words, kept verbatim.
// The log's kind words read through the same table, so one state cannot look
// two ways on one page. The glyph is decoration beside the word, so it is
// aria-hidden and the word stays the message.
export const STATE_SI = {
  waiting: "boarding",
  active: "docked",
  draining: "casting-off",
  parking: "parking",
  parked: "warm",
  releasing: "adrift",
  stuck: "aground",
};
// proto Event.Kind -> the state its word names, in the same vocabulary
const KIND_SI = {
  "lease.requested": "boarding",
  "lease.waiting": "boarding",
  "lease.granted": "docked",
  "lease.reattached": "docked",
  "lease.draining": "casting-off",
  "lease.parking": "parking",
  "lease.parked": "warm",
  "lease.resumed": "docked",
  "lease.unparked": "warn",
  "lease.park_refused": "crit",
  "lease.park_evicted": "warn",
  "lease.lending": "half",
  "lease.lent": "docked",
  "lease.returned": "docked",
  "lease.lend_failed": "warn",
  "lease.released": "adrift",
  "lease.orphaned": "adrift",
  "lease.freed": "adrift",
  "lease.stuck": "aground",
  "lease.juggle": "stale",
  "lease.refused": "crit",
  "squatter.appeared": "crit",
  "squatter.gone": "adrift",
  "probe.failed": "crit",
  "probe.recovered": "up",
  // the card going to and coming back from a VM reads like the lease states:
  // away = aground (attention, the card does not answer), back = up
  "card.passthrough": "aground",
  "card.returned": "up",
  "alert.raised": "warn",
  "alert.cleared": "adrift",
};
export function kindSi(kind) {
  return KIND_SI[kind] || "";
}
// siEl builds the glyph for one state name ("docked", "stored", "warn").
export function siEl(name) {
  const s = el("span", "si si-" + name);
  s.setAttribute("aria-hidden", "true");
  return s;
}
// A reading is "n/a" when the driver reported -1 or the field is absent.
export function isNA(v) {
  return v == null || Number.isNaN(v) || v === -1;
}
// fmt(v) or "n/a" when the driver never reported it.
export function na(v, fmt) {
  return isNA(v) ? "n/a" : fmt(v);
}
export function fmtPct(v, dp = 0) {
  if (isNA(v)) return "n/a";
  return v.toFixed(dp) + "%";
}
export function fmtMS(v, dp = 0) {
  if (isNA(v)) return "n/a";
  if (v >= 1000) return (v / 1000).toFixed(dp == null ? 1 : Math.max(dp, 1)) + "s";
  return v.toFixed(dp) + "ms";
}
export function fmtTok(v) {
  if (isNA(v)) return "n/a";
  if (v >= 1e6) return (v / 1e6).toFixed(2) + "M";
  if (v >= 1e4) return (v / 1e3).toFixed(0) + "k";
  if (v >= 1e3) return (v / 1e3).toFixed(1) + "k";
  return String(Math.round(v));
}
export function fmtClockShort(d) {
  const p = (x) => String(x).padStart(2, "0");
  return p(d.getHours()) + ":" + p(d.getMinutes());
}
// Human size, in the unit that keeps the number below a thousand.
export function fmtBytes(b) {
  if (b == null || !Number.isFinite(b)) return "n/a";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let v = b;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) { v /= 1024; u++; }
  return (u === 0 ? String(Math.round(v)) : v.toFixed(1)) + " " + units[u];
}
// Short local date for chart axes: "Oct 12".
export function fmtDayShort(d) {
  const m = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  return m[d.getMonth()] + " " + d.getDate();
}

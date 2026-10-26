// The passage ticket: a boat ticket with a perforated stub and a rubber stamp.
// Esc and the close button both return focus to the voyage that opened it.
import { fmtGiB, fmtBytes, fmtAge, classWord, stateTone, berthLabel, el } from "./util.js";
import { toast, copyText } from "./ui.js";
const STAMP_WORD = {
  active: "CLEARED",
  waiting: "BOARDING",
  draining: "CASTING OFF",
  parking: "PARKING",
  parked: "WARM",
  releasing: "ADRIFT",
  stuck: "AGROUND",
};
const STAMP_INK = {
  cyan: "ink-cyan",
  amber: "ink-amber",
  magenta: "ink-magenta",
  dim: "ink-amber",
};
export class Ticket {
  constructor(dialog, board) {
    this.dlg = dialog;
    this.board = board;
    this.currentKey = null;
    this.current = null;
    this.f = {
      no: dialog.querySelector("#t-no"),
      vessel: dialog.querySelector("#t-vessel"),
      cls: dialog.querySelector("#t-class"),
      berth: dialog.querySelector("#t-berth"),
      requested: dialog.querySelector("#t-requested"),
      hold: dialog.querySelector("#t-hold"),
      tier: dialog.querySelector("#t-tier"),
      tierRow: dialog.querySelector("#t-tier-row"),
      pos: dialog.querySelector("#t-pos"),
      posRow: dialog.querySelector("#t-pos-row"),
      container: dialog.querySelector("#t-container"),
      containerRow: dialog.querySelector("#t-container-row"),
      cmds: dialog.querySelector("#t-cmds"),
      cmdRow: dialog.querySelector("#t-cmd-row"),
      pid: dialog.querySelector("#t-pid"),
      gpupids: dialog.querySelector("#t-gpupids"),
      issued: dialog.querySelector("#t-issued"),
      stamp: dialog.querySelector("#t-stamp"),
      stamp2: dialog.querySelector("#t-stamp2"),
      stubNo: dialog.querySelector("#t-stub-no"),
      stubCls: dialog.querySelector("#t-stub-class"),
    };
    const closeBtn = dialog.querySelector(".ticket-close");
    closeBtn.addEventListener("click", () => this.close());
    const copyBtn = dialog.querySelector("#t-copy");
    if (copyBtn) copyBtn.addEventListener("click", () => this.copyJson());
    const linkBtn = dialog.querySelector("#t-link");
    if (linkBtn) linkBtn.addEventListener("click", () => this.copyLink());
    dialog.addEventListener("cancel", (e) => {
      e.preventDefault(); // we close ourselves so focus return always runs
      this.close();
    });
    dialog.addEventListener("close", () => {
      const key = this.currentKey;
      this.currentKey = null;
      this.current = null;
      if (key) this.board.focusRow(key);
    });
  }
  open(lease, status, key) {
    this.currentKey = key;
    this.current = { lease, status, key };
    this.fill(lease, status);
    if (typeof this.dlg.showModal === "function") this.dlg.showModal();
    else this.dlg.show();
    this.dlg.querySelector(".ticket-close").focus();
  }
  close() {
    if (this.dlg.open) this.dlg.close();
  }
  async copyJson() {
    if (!this.current) return;
    const ok = await copyText(JSON.stringify(this.current.lease, null, 2));
    toast(ok ? "ticket copied" : "copy failed", ok ? "good" : "bad");
  }
  async copyLink() {
    if (!this.current) return;
    const url = location.origin + location.pathname + location.search + "#ticket=" + encodeURIComponent(this.current.key);
    const ok = await copyText(url);
    toast(ok ? "ticket link copied" : "copy failed", ok ? "good" : "bad");
  }
  refresh() {
    if (this.current) this.fill(this.current.lease, this.current.status);
  }
  fill(lease, status) {
    const f = this.f;
    f.no.textContent = lease.id || "-";
    f.vessel.textContent = lease.engine || "?";
    f.cls.textContent = classWord(lease.priority);
    const sched = (status.gpus || []).find((g) => g.scheduled);
    f.berth.textContent = sched ? "gpu" + sched.index + " - " + sched.name : berthLabel(status);
    f.requested.textContent = lease.needs === 0 ? "whole card" : fmtGiB(lease.needs) + " GiB";
    f.hold.textContent = lease.used > 0 ? fmtGiB(lease.used) + " GiB" : "none measured yet";
    f.pid.textContent = lease.pid ? String(lease.pid) + " / " + String(lease.pid_start || "-") : "- / -";
    f.gpupids.textContent = Array.isArray(lease.gpu_pids) && lease.gpu_pids.length > 0
      ? lease.gpu_pids.join(", ")
      : "-";
    const age = lease.age_seconds;
    f.issued.textContent = age == null ? "-" : age < 1 ? "just now" : fmtAge(age) + " ago";
    const tone = stateTone(lease.state);
    f.stamp.textContent = STAMP_WORD[lease.state] || String(lease.state || "").toUpperCase();
    f.stamp.className = "stamp " + (STAMP_INK[tone] || "ink-amber");
    const contested = (lease.contested || 0) > 0;
    f.stamp2.hidden = !contested;
    if (contested) {
      f.stamp2.textContent = "CONTESTED " + fmtBytes(lease.contested);
      f.stamp2.className = "stamp stamp-second ink-magenta";
    }
    this.fillTier(lease, status);
    this.fillQueue(lease);
    this.fillContainer(lease);
    this.fillCmds(lease);
    f.stubNo.textContent = f.no.textContent;
    f.stubCls.textContent = f.cls.textContent;
  }
  // TIER is where the weights live: hot on the card, warm parked in RAM.
  // The est. RAM is the engine's learned footprint, a measurement of past
  // runs, never a reservation.
  fillTier(lease, status) {
    const st = lease.state;
    switch (st) {
      case "active":
      case "draining":
        this.f.tierRow.hidden = false;
        this.f.tier.textContent = "HOT - running on the card";
        break;
      case "parking":
        this.f.tierRow.hidden = false;
        this.f.tier.textContent = "PARKING - the weights are moving to RAM";
        break;
      case "parked": {
        this.f.tierRow.hidden = false;
        let fp = "";
        for (const x of (status && status.footprints) || []) {
          if (x.engine === lease.engine && x.estimated_vram_bytes > 0) fp = fmtBytes(x.estimated_vram_bytes);
        }
        this.f.tier.textContent = "WARM - parked in RAM"
          + (lease.parked_since != null ? " for " + fmtAge(lease.parked_since) : "")
          + (fp ? ", holding " + fp + " (est., from past runs)" : "");
        break;
      }
      default:
        this.f.tierRow.hidden = true;
        return;
    }
  }
  // QUEUE / EST. says where a boarding request sits and how long the node
  // expects it to wait. The row renders only when the status payload named
  // position or estimate_s; an older node sends neither and nothing shows.
  fillQueue(lease) {
    const has = lease.position != null || lease.estimate_s != null;
    this.f.posRow.hidden = !has;
    if (has) {
      const bits = [];
      if (lease.position != null) bits.push("position " + lease.position);
      if (lease.estimate_s != null) bits.push("est. " + fmtAge(lease.estimate_s));
      this.f.pos.textContent = bits.join(", ");
    }
  }
  // CONTAINER: every container the engine runs in, by ID: one for a
  // --container engine, the whole set for a Compose project.
  fillContainer(lease) {
    const ids = (lease.containers || []).join(", ");
    this.f.containerRow.hidden = !ids;
    if (ids) this.f.container.textContent = ids;
  }
  // The terminal row: the exact orders, as copy buttons. The ticket is the
  // operator's seat at the counter, not the node's stick; the harbor reads
  // and the terminal acts, so these buttons copy and never send.
  fillCmds(lease) {
    const cmds = [];
    if (lease.state === "parked") cmds.push(["resume", "berth resume " + lease.engine]);
    else if (lease.state === "active" || lease.state === "draining" || lease.state === "parking") {
      cmds.push(["stop", "berth stop " + lease.engine]);
    }
    if (lease.state === "stuck" || lease.state === "releasing") {
      cmds.push(["reclaim", "berth reclaim " + lease.id]);
    }
    this.f.cmdRow.hidden = cmds.length === 0;
    if (!cmds.length) return;
    this.f.cmds.replaceChildren();
    for (const [label, command] of cmds) {
      const btn = el("button", "tcmd", label, el("span", "tcmd-word", command));
      btn.type = "button";
      btn.setAttribute("aria-label", "copy: " + command);
      btn.title = command;
      btn.addEventListener("click", async () => {
        const ok = await copyText(command);
        toast(ok ? command + " copied" : "copy failed", ok ? "good" : "bad");
      });
      this.f.cmds.append(btn);
    }
  }
}

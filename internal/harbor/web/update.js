// The "Update available" notice in the header: the cached release check
// from the harbor, one read-only request, nothing that changes state.
// The page makes no request of its own; the harbor is the one that asks
// the release page, at most once a day, and only while its update check
// is on.
import { $, el } from "./util.js";
import { copyText, toast } from "./ui.js";

// The server re-asks at most once a day; five minutes keeps an open page
// honest without asking more.
const CHECK_MS = 5 * 60 * 1000;

async function refreshNotice() {
  let j;
  try {
    const r = await fetch("v1/update", { cache: "no-store" });
    if (!r.ok) return; // demo mode has no API, and neither does a dead node
    j = await r.json();
  } catch {
    return;
  }
  const n = $("update-notice");
  if (!n) return;
  n.replaceChildren();
  n.hidden = !(j && j.enabled && j.newer);
  if (n.hidden) return;

  // The release name comes from the operator's own daemon; the command
  // is literal. API strings reach the DOM as textContent, as always.
  const a = el("a", "update-link", j.latest);
  a.href = j.url || "#";
  a.title = j.url || "";
  const btn = el("button", "copy-btn", "berth update");
  btn.type = "button";
  btn.title = "copy the command that replaces the binary";
  btn.addEventListener("click", async () => {
    await copyText("berth update");
    toast("copied: berth update");
  });
  n.append("Update available: ", a, " · ", btn);
}

refreshNotice();
setInterval(refreshNotice, CHECK_MS);

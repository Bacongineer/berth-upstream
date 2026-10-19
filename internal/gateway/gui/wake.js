// The wake page: show who holds the card, start the engine only when asked,
// say what the start waits for, and open the engine's web UI once it answers.
(() => {
  const name = document.body.dataset.engine;
  if (!name) return;
  const base = "/gui/" + encodeURIComponent(name);
  const $ = (id) => document.getElementById(id);
  const status = $("status"), detail = $("detail"), card = $("card");
  const start = $("start"), meta = $("meta"), elapsed = $("elapsed");
  let since = 0;

  setInterval(() => {
    if (since) elapsed.textContent = Math.round((Date.now() - since) / 1000) + " s";
  }, 1000);

  function showCard(holds) {
    card.replaceChildren(...(holds || []).map((h) => {
      const li = document.createElement("li");
      const who = document.createElement("strong");
      who.textContent = h.name;
      const what = document.createElement("span");
      what.textContent = h.pinned ? "held, never stopped" : h.state;
      what.className = "chip " + (h.pinned ? "pinned" : h.state);
      li.append(who, what);
      return li;
    }));
  }

  function render(s) {
    document.body.className = s.state;
    status.textContent = s.message || s.state;
    detail.textContent = s.detail || "";
    showCard(s.card);
    const idle = s.state === "offline" || s.state === "failed";
    start.hidden = !idle;
    start.textContent = s.state === "failed" ? "Try again" : "Start " + name;
    meta.hidden = idle;
    if (!idle && !since) since = Date.now();
    if (idle) since = 0;
  }

  async function poll() {
    let s;
    try {
      const r = await fetch(base + "/state", { cache: "no-store" });
      s = await r.json();
    } catch {
      status.textContent = "The gateway is not answering; retrying ...";
      setTimeout(poll, 2000);
      return;
    }
    render(s);
    if (s.state === "up") {
      location.replace(s.url);
      return;
    }
    // Offline is polled slowly so the card stays current; a start fast.
    setTimeout(poll, s.state === "offline" || s.state === "failed" ? 3000 : 1000);
  }

  start.addEventListener("click", async () => {
    start.hidden = true;
    since = Date.now();
    meta.hidden = false;
    status.textContent = "Asking berth for the card ...";
    try {
      const r = await fetch(base + "/wake", { method: "POST" });
      render(await r.json());
    } catch {
      status.textContent = "The gateway is not answering";
    }
  });

  poll();
})();

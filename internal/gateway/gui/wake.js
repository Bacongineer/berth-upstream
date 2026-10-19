// The wake page: ask the gateway to start the engine, poll its state, and
// go to the engine's web UI once it answers.
(() => {
  const name = document.body.dataset.engine;
  if (!name) return;
  const base = "/gui/" + encodeURIComponent(name);
  const status = document.getElementById("status");
  const detail = document.getElementById("detail");
  const elapsed = document.getElementById("elapsed");
  const retry = document.getElementById("retry");
  let started = Date.now();
  let timer = null;

  const tick = () => { elapsed.textContent = Math.round((Date.now() - started) / 1000) + " s"; };
  setInterval(tick, 1000);

  const wake = () => fetch(base + "/wake", { method: "POST" }).catch(() => {});

  async function poll() {
    let s;
    try {
      const r = await fetch(base + "/state", { cache: "no-store" });
      s = await r.json();
    } catch {
      status.textContent = "The gateway is not answering; retrying ...";
      timer = setTimeout(poll, 2000);
      return;
    }
    switch (s.state) {
      case "up":
        status.textContent = "Ready. Opening " + name + " ...";
        document.body.classList.add("ready");
        location.replace(s.url);
        return;
      case "failed":
        status.textContent = name + " did not start" + (s.code ? " (" + s.code + ")" : "");
        detail.textContent = s.detail || "";
        document.body.classList.add("failed");
        retry.hidden = false;
        return;
      case "down":
        await wake();
        break;
      default:
        status.textContent = "Starting " + name + " ...";
    }
    detail.textContent = s.detail || "";
    timer = setTimeout(poll, 1000);
  }

  retry.addEventListener("click", async () => {
    retry.hidden = true;
    document.body.classList.remove("failed");
    started = Date.now();
    status.textContent = "Asking berth for the card ...";
    detail.textContent = "";
    clearTimeout(timer);
    await wake();
    poll();
  });

  wake().then(poll);
})();

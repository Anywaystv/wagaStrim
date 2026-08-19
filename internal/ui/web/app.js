// No framework. Fetch plus a few listeners is the whole client.
// A failed request used to leave the page silently unchanged, which reads as the
// button being broken. Say so on the button itself instead.
const post = (url, body, btn) =>
  fetch(url, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  }).then((res) => {
    if (!res.ok) throw new Error(res.status);
    return res;
  }).catch((err) => {
    if (btn) {
      const was = btn.textContent;
      btn.textContent = "Failed";
      setTimeout(() => (btn.textContent = was), 2500);
    }
    console.error(url, err);
    throw err;
  });

document.addEventListener("click", (ev) => {
  const btn = ev.target.closest("button");
  if (!btn) return;

  if (btn.hasAttribute("data-reveal")) {
    const box = btn.parentElement.querySelector(".secret");
    const hidden = box.classList.toggle("masked");
    box.textContent = hidden ? "••••••••••••••••" : box.dataset.secret;
    btn.textContent = hidden ? "Show" : "Hide";
    return;
  }

  if (btn.hasAttribute("data-copy")) {
    const box = btn.parentElement.querySelector(".secret");
    navigator.clipboard.writeText(box.dataset.secret);
    btn.textContent = "Copied for " + box.dataset.destination;
    setTimeout(() => (btn.textContent = "Copy"), 1800);
    return;
  }

  if (btn.id === "add") {
    const label = document.getElementById("label").value.trim() || "Camera";
    post("/api/ingests", { label }, btn).then(() => location.reload());
    return;
  }

  const removeID = btn.getAttribute("data-remove");
  if (removeID) {
    post("/api/ingests/remove", { id: removeID }, btn).then(() => location.reload());
  }
});

document.addEventListener("input", (ev) => {
  const slider = ev.target.closest("[data-delay]");
  if (!slider) return;

  slider.closest("section").querySelector("[data-delay-out]").textContent = slider.value;
});

document.addEventListener("change", (ev) => {
  const slider = ev.target.closest("[data-delay]");
  if (!slider) return;

  post("/api/ingests/delay", {
    id: slider.getAttribute("data-delay"),
    delayMs: Number(slider.value),
  }).catch(() => {});
});

// Poll rather than stream: one small request a second costs less than holding a
// socket open for a page that is usually not being looked at.
async function poll() {
  const res = await fetch("/api/stats");
  if (!res.ok) return;

  const all = await res.json();

  for (const card of document.querySelectorAll("section[data-id]")) {
    const snap = all[card.dataset.id];
    if (!snap) continue;

    const badge = card.querySelector("[data-status]");
    badge.textContent = snap.live ? "live" : "idle";
    badge.className = "status-button" + (snap.live ? " good" : "");

    const line = card.querySelector("[data-stats]");
    if (!snap.live) {
      line.textContent = "Waiting for a publisher.";
      continue;
    }

    const parts = [`${snap.bitrateKbps} kbps`, `up ${snap.liveSeconds}s`];
    if (snap.late) parts.push(`${snap.late} late`);
    if (snap.dropped) parts.push(`${snap.dropped} dropped`);

    line.textContent = parts.join("  ·  ") + (snap.advice ? "  —  " + snap.advice : "");
  }
}

setInterval(() => poll().catch(() => {}), 1000);
poll().catch(() => {});

document.getElementById("check").addEventListener("click", async (ev) => {
  const out = document.getElementById("reach");
  out.textContent = "Checking…";

  try {
    const res = await fetch("/api/reachability");
    const r = await res.json();
    const lan = r.lanHosts.length ? ` On this network: ${r.lanHosts.join(", ")}.` : "";
    const pub = r.publicHost ? `Public address ${r.publicHost}. ` : "";

    out.textContent = pub + r.note + lan +
      ` Forward udp/${r.mediaPort} and tcp/${r.signalPort}.`;

    if (r.publicHost) setTimeout(() => location.reload(), 1200);
  } catch (err) {
    out.textContent = "Could not check: " + err;
    ev.target.textContent = "Retry";
  }
});

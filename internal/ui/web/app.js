// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

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
  if (slider) {
    post("/api/ingests/delay", {
      id: slider.getAttribute("data-delay"),
      delayMs: Number(slider.value),
    }).catch(() => {});
    return;
  }

  const group = ev.target.closest("[data-group]");
  if (group) {
    post("/api/ingests/group", {
      id: group.getAttribute("data-group"),
      group: group.value.trim(),
    }).catch(() => {});
    return;
  }

  const codecs = ev.target.closest("[data-codecs]");
  if (codecs) {
    post("/api/ingests/codecs", {
      id: codecs.getAttribute("data-codecs"),
      codecs: [...codecs.querySelectorAll("input:checked")].map((i) => i.value),
    }).then(() => location.reload()).catch(() => {});
    return;
  }

  const label = ev.target.closest("[data-label]");
  if (label) {
    post("/api/ingests/label", {
      id: label.getAttribute("data-label"),
      label: label.value.trim() || "Camera",
    }).catch(() => {});
  }
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
    if (snap.codec) parts.push(snap.codec);
    if (snap.path) parts.push(snap.path);
    if (snap.switches) parts.push(`${snap.switches} network ${snap.switches === 1 ? "change" : "changes"}`);
    if (snap.pathsTotal) parts.push(`${snap.pathsLive}/${snap.pathsTotal} paths usable`);
    if (snap.late) parts.push(`${snap.late} late`);
    if (snap.dropped) parts.push(`${snap.dropped} dropped`);

    line.textContent = parts.join(", ") + (snap.advice ? ". " + snap.advice : "");
  }

  // Aggregate, because what limits a multi-camera setup is the total, not any
  // one stream. No invented capacity model: the number is shown, not judged.
  const live = Object.values(all).filter((s) => s.live);
  const total = live.reduce((sum, s) => sum + s.bitrateKbps, 0);
  const mediaStatus = document.getElementById("media-status");
  mediaStatus.textContent = live.length ? "live" : "no media";
  mediaStatus.className = "status-button " + (live.length ? "good" : "warn");
  document.getElementById("total").textContent = live.length
    ? `${live.length} streaming, ${(total / 1000).toFixed(1)} Mbps total`
    : "No cameras streaming.";
}

// Nothing a person can see changes while the page is hidden, and this one sits
// behind OBS for hours.
setInterval(() => {
  if (!document.hidden) poll().catch(() => {});
}, 1000);
poll().catch(() => {});

document.getElementById("check").addEventListener("click", async (ev) => {
  const out = document.getElementById("reach");
  const status = document.getElementById("reach-status");
  out.replaceChildren();
  status.textContent = "Checking";

  try {
    const res = await fetch("/api/reachability");
    const r = await res.json();
    const lines = [];
    if (r.publicHost) lines.push(["Internet: ", r.publicHost, ""]);
    if (r.lanHosts?.length) lines.push(["Wi-Fi / VPN: ", r.lanHosts.join(", "), ""]);
    lines.push(
      ["Ports: ", `TCP ${r.signalPort}`, ` setup · UDP ${r.mediaPort} media`],
    );
    if (r.socketNote?.includes("capped")) lines.push(["Receive buffer limited — see details.", "", ""]);

    out.replaceChildren();
    for (const [label, value, detail] of lines) {
      const line = document.createElement("span");
      const bold = document.createElement("strong");
      bold.textContent = value;
      line.append(label, bold, detail);
      out.append(line);
    }
    const details = document.createElement("details");
    const summary = document.createElement("summary");
    summary.textContent = "Connection details";
    const help = document.createElement("p");
    help.textContent = [r.note, "Allow both ports in your firewall; forward them if using a router.", r.socketNote]
      .filter(Boolean).join(" ");
    details.append(summary, help);
    out.append(details);
    status.textContent = r.publicHost ? "Ports not verified. Test on mobile data." : "No public IP found. Try a local IP.";

    // Refresh the links without reloading away the check result.
    if (r.publicHost) {
      const host = r.publicHost.includes(":") ? `[${r.publicHost}]` : r.publicHost;
      for (const box of document.querySelectorAll(".secret[data-secret]")) {
        const link = new URL(box.dataset.secret);
        if (link.protocol === "https:" || link.protocol === "whips:") continue;
        link.host = `${host}:${r.signalPort}`;
        box.dataset.secret = link.href;
        if (!box.classList.contains("masked")) box.textContent = link.href;
      }
    }
  } catch (err) {
    out.textContent = "Could not check: " + err;
    status.textContent = "Check failed.";
    ev.target.textContent = "Retry";
  }
});

// Autostart state comes from the platform, not from the config file, so the tick
// cannot claim an entry that was removed outside this app.
const autostartBox = document.getElementById("autostart");

for (const [scope, label] of [["lan", "LAN"], ["remote", "Remote"]]) {
  const checkbox = document.getElementById(`control-${scope}`);
  const note = document.getElementById(`control-${scope}-note`);
  checkbox.addEventListener("change", async () => {
    const on = checkbox.checked;
    checkbox.disabled = true;
    note.textContent = "Saving…";
    try {
      await post(`/api/control/${scope}`, { on });
      note.textContent = `${label} access ${on ? "enabled" : "disabled"}.`;
    } catch {
      checkbox.checked = !on;
      note.textContent = `Could not save ${label.toLowerCase()} access. Try again.`;
    } finally {
      checkbox.disabled = false;
    }
  });
}
const autostartNote = document.getElementById("autostart-note");

function showAutostart(state) {
  autostartBox.checked = state.on;

  const parts = [];
  if (state.stale) {
    parts.push(
      "The login entry points at a different file, which happens after moving or " +
      "upgrading the binary. Untick and tick again to repair it."
    );
  }
  if (state.note) parts.push(state.note);

  autostartNote.textContent = parts.join(" ");
}

fetch("/api/autostart").then((r) => r.json()).then(showAutostart).catch(() => {});

autostartBox.addEventListener("change", () => {
  post("/api/autostart", { on: autostartBox.checked })
    .then((r) => r.json())
    .then(showAutostart)
    .catch(() => {
      autostartBox.checked = !autostartBox.checked;
      autostartNote.textContent = "Could not change the login entry.";
    });
});

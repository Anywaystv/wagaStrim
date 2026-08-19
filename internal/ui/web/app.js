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

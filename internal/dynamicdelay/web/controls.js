// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

(() => {
  const render = (field) => {
    field.querySelector("[data-dynamic-options]").hidden = !field.querySelector("[data-dynamic-enabled]").checked;
    field.querySelector("[data-dynamic-out]").textContent = field.querySelector("[data-dynamic-maximum]").value;
    field.querySelector("[data-dynamic-speedout]").textContent = field.querySelector("[data-dynamic-speed]").value;
  };

  document.addEventListener("input", (event) => {
    const field = event.target.closest("[data-dynamic-delay]");
    if (field) render(field);
    const normal = event.target.closest("[data-delay]");
    if (!normal) return;
    const options = normal.closest("section").querySelector("[data-dynamic-delay]");
    const maximum = options.querySelector("[data-dynamic-maximum]");
    maximum.min = Math.max(1, Number(normal.value));
    maximum.value = Math.max(Number(maximum.min), Number(maximum.value));
    render(options);
  });

  document.addEventListener("change", async (event) => {
    const field = event.target.closest("[data-dynamic-delay]");
    if (!field || field.disabled) return;
    const enabled = field.querySelector("[data-dynamic-enabled]");
    const maximum = field.querySelector("[data-dynamic-maximum]");
    const jump = field.querySelector("[data-dynamic-jump]");
    const speed = field.querySelector("[data-dynamic-speed]");
    const note = field.querySelector("[data-dynamic-note]");
    field.disabled = true;
    note.textContent = "Saving...";
    render(field);
    try {
      const response = await fetch("/api/ingests/dynamic-delay", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ id: field.dataset.dynamicDelay, dynamicDelay: enabled.checked,
          maxDelayMs: Number(maximum.value), jumpAtMaximum: jump.checked,
          catchUpMsPerSecond: Number(speed.value) }),
      });
      if (!response.ok) throw new Error("save failed");
      field.dataset.enabled = String(enabled.checked);
      field.dataset.maximum = maximum.value;
      field.dataset.jump = String(jump.checked);
      field.dataset.speed = speed.value;
      note.textContent = "Saved.";
    } catch {
      enabled.checked = field.dataset.enabled === "true";
      maximum.value = field.dataset.maximum;
      jump.checked = field.dataset.jump === "true";
      speed.value = field.dataset.speed;
      note.textContent = "Could not save dynamic delay. Your previous settings are still shown.";
      render(field);
    } finally {
      field.disabled = false;
    }
  });
})();

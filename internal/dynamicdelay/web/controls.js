// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

(() => {
  const render = (field) => {
    field.querySelector("[data-dynamic-options]").hidden = !field.querySelector("[data-dynamic-enabled]").checked;
    field.querySelector("[data-dynamic-manual]").hidden = field.querySelector("[data-dynamic-auto]").checked;
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
    const normal = event.target.closest("[data-delay]");
    const field = normal ? normal.closest("section").querySelector("[data-dynamic-delay]")
      : event.target.closest("[data-dynamic-delay]");
    if (!field || (!normal && field.disabled)) return;
    const section = field.closest("section");
    const delay = section.querySelector("[data-delay]");
    if (delay.disabled) return;
    const enabled = field.querySelector("[data-dynamic-enabled]");
    const maximum = field.querySelector("[data-dynamic-maximum]");
    const jump = field.querySelector("[data-dynamic-jump]");
    const speed = field.querySelector("[data-dynamic-speed]");
    const automatic = field.querySelector("[data-dynamic-auto]");
    const note = field.querySelector("[data-dynamic-note]");
    const disabled = field.disabled;
    field.disabled = delay.disabled = true;
    note.textContent = "Saving...";
    render(field);
    try {
      const response = await fetch(normal ? "/api/ingests/delay" : "/api/ingests/dynamic-delay", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(normal ? { id: field.dataset.dynamicDelay, delayMs: Number(delay.value) }
          : { id: field.dataset.dynamicDelay, dynamicDelay: enabled.checked,
          maxDelayMs: Number(maximum.value), jumpAtMaximum: jump.checked,
          catchUpMsPerSecond: Number(speed.value), autoCatchUp: automatic.checked }),
      });
      if (!response.ok) throw new Error("save failed");
      if (normal) {
        delay.defaultValue = delay.value;
        maximum.value = String(Math.max(Number(field.dataset.maximum), Number(delay.value), 1));
      }
      field.dataset.enabled = String(enabled.checked);
      field.dataset.maximum = maximum.value;
      field.dataset.jump = String(jump.checked);
      field.dataset.speed = speed.value;
      field.dataset.auto = String(automatic.checked);
      note.textContent = "Saved.";
    } catch {
      delay.value = delay.defaultValue;
      maximum.min = Math.max(1, Number(delay.value));
      enabled.checked = field.dataset.enabled === "true";
      maximum.value = field.dataset.maximum;
      jump.checked = field.dataset.jump === "true";
      speed.value = field.dataset.speed;
      automatic.checked = field.dataset.auto === "true";
      note.textContent = "Could not save delay. Your previous settings are still shown.";
    } finally {
      render(field);
      section.querySelector("[data-delay-out]").textContent = delay.value;
      field.disabled = disabled;
      delay.disabled = false;
    }
  });
})();

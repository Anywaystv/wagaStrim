// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

test("dynamic controls persist per camera and roll back a failed save", async () => {
  const handlers = {};
  const nodes = {
    enabled: { checked: false }, maximum: { value: "10000" }, jump: { checked: true },
    speed: { value: "10" }, speedout: {}, options: { hidden: true }, out: {}, note: {},
  };
  const field = {
    dataset: { dynamicDelay: "camera", enabled: "false", maximum: "10000", jump: "true", speed: "10" },
    disabled: false,
    querySelector: selector => nodes[selector.match(/data-dynamic-(\w+)/)[1]],
  };
  const requests = [];
  let save;
  runInNewContext(readFileSync(new URL("web/controls.js", import.meta.url), "utf8"), {
    document: { addEventListener: (name, handler) => { handlers[name] = handler; } },
    fetch: async (path, options) => {
      requests.push({ path, body: JSON.parse(options.body) });
      return new Promise(resolve => { save = resolve; });
    },
  });
  const event = { target: { closest: selector => selector === "[data-dynamic-delay]" ? field : null } };
  nodes.enabled.checked = true;
  handlers.input(event);
  assert.equal(nodes.options.hidden, false);
  const pending = handlers.change(event);
  assert.equal(field.disabled, true);
  await handlers.change(event);
  assert.equal(requests.length, 1, "a pending save cannot be overwritten");
  assert.deepEqual(requests[0], {
    path: "/api/ingests/dynamic-delay",
    body: { id: "camera", dynamicDelay: true, maxDelayMs: 10000, jumpAtMaximum: true, catchUpMsPerSecond: 10 },
  });
  save({ ok: true });
  await pending;
  assert.equal(field.disabled, false);
  assert.equal(field.dataset.enabled, "true");
  nodes.enabled.checked = false;
  nodes.maximum.value = "4000";
  nodes.jump.checked = false;
  nodes.speed.value = "100";
  handlers.input(event);
  assert.equal(nodes.speedout.textContent, "100");
  const failed = handlers.change(event);
  save({ ok: false });
  await failed;
  assert.equal(field.disabled, false);
  assert.equal(nodes.enabled.checked, true);
  assert.equal(nodes.options.hidden, false);
  assert.equal(nodes.maximum.value, "10000");
  assert.equal(nodes.jump.checked, true);
  assert.equal(nodes.speed.value, "10");
  assert.equal(nodes.speedout.textContent, "10");
  assert.match(nodes.note.textContent, /Could not save/);
  const normal = { value: "5000", closest: () => ({ querySelector: () => field }) };
  nodes.maximum.value = "4000";
  handlers.input({ target: { closest: selector => selector === "[data-delay]" ? normal : null } });
  assert.equal(Number(nodes.maximum.min), 5000);
  assert.equal(Number(nodes.maximum.value), 5000);
  nodes.speed.value = "50";
  const faster = handlers.change(event);
  assert.equal(requests.at(-1).body.catchUpMsPerSecond, 50);
  save({ ok: true });
  await faster;
  assert.equal(field.dataset.speed, "50");
});

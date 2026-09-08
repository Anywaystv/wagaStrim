// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const read = (path) => readFileSync(new URL(`../${path}`, import.meta.url), "utf8");

// These check client state transitions, not browser rendering or media decoding.
function element(tag = "span") {
  return {
    tag, textContent: "", children: [],
    append(...items) { this.children.push(...items); },
    replaceChildren() { this.children = []; this.textContent = ""; },
    addEventListener(event, handler) { this[event] = handler; },
  };
}

test("reachability stays visible and refreshes masked and visible links", async () => {
  const nodes = Object.fromEntries(
    ["check", "reach", "reach-status", "total", "media-status", "autostart", "autostart-note"].map(id => [id, element()]),
  );
  const links = [true, false].map(masked => ({
    dataset: { secret: `${masked ? "whip" : "http"}://127.0.0.1:7331/${masked ? "whip/s_test" : "player/r_test"}` },
    textContent: masked ? "masked" : "",
    classList: { contains: () => masked },
  }));
  let publicHost = "203.0.113.5";
  let fail = false;
  runInNewContext(read("internal/ui/web/app.js"), {
    document: {
      addEventListener() {},
      getElementById: id => nodes[id],
      createElement: element,
      querySelectorAll: selector => selector === ".secret[data-secret]" ? links : [],
    },
    fetch: async path => {
      if (path === "/api/reachability" && fail) throw new Error("offline");
      return { ok: true, json: async () => path === "/api/reachability" ? {
        publicHost, lanHosts: ["192.168.1.2"], signalPort: 7331, mediaPort: 7332,
        note: "Ports not verified.", socketNote: "<img src=x onerror=alert(1)>",
      } : {} };
    },
    setInterval() {},
    setTimeout() { assert.fail("the check must not schedule a page reload"); },
    URL, console,
  });

  for (const host of ["203.0.113.5", "2001:db8::1", ""]) {
    publicHost = host;
    const before = links[0].dataset.secret;
    const pending = nodes.check.click({ target: nodes.check });
    assert.equal(nodes["reach-status"].textContent, "Checking");
    await pending;
    assert.equal(nodes["reach-status"].textContent, host ? "Address checked." : "No public IP found.");
    const rows = nodes.reach.children;
    const bold = rows.flatMap(row => row.children.filter(child => child.tag === "strong").map(child => child.textContent));
    assert.ok(bold.includes("192.168.1.2"));
    assert.ok(bold.includes("TCP 7331"));
    assert.ok(bold.includes("UDP 7332"));
    assert.equal(rows.at(-1).children[0], "<img src=x onerror=alert(1)>");
    assert.equal(links[0].textContent, "masked");
    assert.equal(new URL(links[0].dataset.secret).pathname, "/whip/s_test");
    assert.equal(links[1].textContent, links[1].dataset.secret);
    if (host) {
      assert.ok(bold.includes(host));
      assert.equal(new URL(links[0].dataset.secret).hostname, host.includes(":") ? `[${host}]` : host);
    } else {
      assert.equal(links[0].dataset.secret, before);
    }
  }
  fail = true;
  await nodes.check.click({ target: nodes.check });
  assert.equal(nodes["reach-status"].textContent, "Check failed.");
  assert.match(nodes.reach.textContent, /offline/);
});

test("camera summary is in the header and reachability has a privacy warning", () => {
  const html = read("internal/ui/web/index.html");
  const header = html.match(/<header>([\s\S]*?)<\/header>/)[1];
  assert.equal((html.match(/id="total"/g) || []).length, 1);
  assert.ok(header.includes('id="total"'));
  assert.ok(!header.includes('id="reach-status"'));
  assert.ok(html.includes("(don't show on stream)"));
});

test("header media status follows live and idle stats", async () => {
  const nodes = Object.fromEntries(
    ["check", "reach", "reach-status", "total", "media-status", "autostart", "autostart-note"].map(id => [id, element()]),
  );
  let stats = {};
  const context = {
    document: { addEventListener() {}, getElementById: id => nodes[id], querySelectorAll: () => [] },
    fetch: async () => ({ ok: true, json: async () => stats }),
    setInterval() {}, console,
  };
  runInNewContext(read("internal/ui/web/app.js"), context);
  for (const live of [true, false]) {
    stats = { camera: { live, bitrateKbps: 5000 } };
    await context.poll();
    assert.equal(nodes["media-status"].textContent, live ? "live" : "no media");
    assert.equal(nodes["media-status"].className, live ? "status-button good" : "status-button warn");
  }
});

test("successful audio playback dismisses only the audio prompt", async () => {
  let blocked = true;
  const video = { play: () => blocked ? Promise.reject(new Error("blocked")) : Promise.resolve() };
  const msg = { textContent: "Connecting", hidden: false };
  const body = {};
  class Peer {
    iceGatheringState = "complete";
    localDescription = { sdp: "" };
    addTransceiver() {}
    async createOffer() { return {}; }
    async setLocalDescription() {}
    async setRemoteDescription() { this.ontrack({ streams: [{}] }); }
    getReceivers() { return []; }
  }
  const script = read("internal/egress/web/player.html").match(/<script>([\s\S]*?)<\/script>/)[1];
  runInNewContext(script, {
    location: { pathname: "/player/test" },
    document: { body, getElementById: id => id === "v" ? video : msg },
    RTCPeerConnection: Peer,
    fetch: async () => ({ ok: true, headers: { get: () => "/whep/resource/test" }, text: async () => "" }),
    addEventListener() {}, setTimeout,
  });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(msg.textContent, "Click to start audio");
  assert.equal(msg.hidden, false);
  await body.onclick();
  assert.equal(msg.hidden, false);
  blocked = false;
  await body.onclick();
  assert.equal(msg.hidden, true);
  msg.textContent = "Reconnecting";
  msg.hidden = false;
  await body.onclick();
  assert.equal(msg.hidden, false);
});

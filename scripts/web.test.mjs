// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";
import { clocksChanged } from "../internal/dynamicdelay/web/player.js";

const read = (path) => readFileSync(new URL(`../${path}`, import.meta.url), "utf8");

class Peer {
  iceGatheringState = "complete";
  localDescription = { sdp: "" };
  addTransceiver() {}
  close() {}
  async createOffer() { return {}; }
  async setLocalDescription() {}
  async setRemoteDescription() { this.ontrack({ streams: [{}] }); }
  getReceivers() { return []; }
}

// These check client state transitions, not browser rendering or media decoding.
function element(tag = "span") {
  return {
    tag, textContent: "", children: [],
    append(...items) { this.children.push(...items); },
    replaceChildren() { this.children = []; this.textContent = ""; },
    addEventListener(event, handler) { this[event] = handler; },
  };
}

const dashboardNodes = () => Object.fromEntries(
  [...read("internal/ui/web/index.html").matchAll(/\bid="([^"]+)"/g)].map(([, id]) => [id, element()]),
);

test("reachability stays visible and refreshes masked and visible links", async () => {
  const nodes = dashboardNodes();
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
      querySelectorAll: selector => selector === ".camera-card .secret[data-secret]" ? links : [],
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
    assert.equal(nodes["reach-status"].textContent, host
      ? "Ports not verified. Test on mobile data." : "No public IP found. Try a local IP.");
    const rows = nodes.reach.children;
    const bold = rows.flatMap(row => row.children.filter(child => child.tag === "strong").map(child => child.textContent));
    assert.ok(bold.includes("192.168.1.2"));
    assert.ok(bold.includes("TCP 7331"));
    assert.ok(rows.at(-2).children[2].includes("UDP 7332"));
    const help = rows.at(-1).children[1];
    assert.ok(help.textContent.includes("<img src=x onerror=alert(1)>"));
    assert.equal(help.children.length, 0);
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
  const nodes = dashboardNodes();
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
  const script = read("internal/egress/web/player.html").match(/<script>([\s\S]*?)<\/script>/)[1];
  runInNewContext(script, {
    location: { pathname: "/player/test" },
    document: { body, getElementById: id => id === "v" ? video : msg },
    RTCPeerConnection: Peer,
    fetch: async () => ({ ok: true, json: async () => ({ dynamicDelay: false }), headers: { get: () => "/whep/resource/test" }, text: async () => "" }),
    addEventListener() {}, setTimeout(callback, delay) { return setTimeout(callback, delay).unref(); },
    clearTimeout, AbortSignal,
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

test("a relay clock correction reconnects the custom player once with fresh references", async () => {
  const script = read("internal/egress/web/player.html").match(/<script>([\s\S]*?)<\/script>/)[1];
  let settings = { dynamicDelay: false, clocks: [
    { kind: "video", mime: "video/H264", rate: 90000, timestamp: 90000, referenceMs: 1000 },
    { kind: "audio", mime: "audio/opus", rate: 48000, timestamp: 48000, referenceMs: 1000 },
  ] };
  let closed = 0, attached = 0, removed = 0;
  const timers = [];
  const player = { attach: async () => { attached++; }, close() { closed++; }, update() {} };
  const context = {
    location: { pathname: "/player/test" },
    document: { body: {}, getElementById: () => ({ play: async () => {}, hidden: true }) },
    RTCPeerConnection: Peer, AbortSignal,
    fetch: async (path, options) => {
      if (options?.method === "DELETE") removed++;
      return { ok: true, json: async () => structuredClone(settings),
        headers: { get: () => "/whep/resource/test" }, text: async () => "" };
    },
    addEventListener() {}, setTimeout: callback => { timers.push(callback); return callback; },
    clearTimeout() {},
    testModule: { clocksChanged, supported: () => true, createPlayer: () => player }, testPlayer: player,
  };
  runInNewContext(script, context);
  await new Promise(resolve => setImmediate(resolve));
  // Start from an attached custom player without mocking media decoding in this state test.
  runInNewContext("settings.dynamicDelay = true; active.player = testPlayer; playbackModule = testModule", context);
  settings.dynamicDelay = true;
  for (let step = 0; step < 4; step++) {
    settings.clocks[1].referenceMs -= 20;
    await timers.shift()();
    assert.equal(closed, 0, "small changes should stay within the sync tolerance");
  }
  settings.clocks[1].referenceMs -= 40;
  await timers.shift()();
  assert.equal(closed, 1, "drift must be measured from the player, not the last poll");
  timers.shift()();
  await new Promise(resolve => setImmediate(resolve));
  await timers.shift()();
  assert.equal(closed, 1, "fresh clocks must not reconnect repeatedly");
  closed = attached = removed = 0;
  settings.clocks[1].referenceMs -= 2095.41;
  await timers.shift()();
  assert.equal(closed, 1);
  assert.equal(removed, 1, "the stale WHEP session must be released");
  timers.shift()();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(attached, 1, "the replacement player must attach");
  assert.equal(runInNewContext("settings.clocks[1].referenceMs", context), settings.clocks[1].referenceMs);
  await timers.shift()();
  assert.equal(closed, 1, "unchanged references must not cause a reconnect loop");
});

test("settings polls back off within the shared request budget and keep playback running", async () => {
  const script = read("internal/egress/web/player.html").match(/<script>([\s\S]*?)<\/script>/)[1];
  let now = 0, tokens = 20, limited = false, denials = 0, lateDenials = 0, seed = 1, stopping = false;
  const timers = [], notices = [], hide = [], lastRead = Array(64).fill(0), blockedUntil = Array(64).fill(0);
  class WatchedPeer extends Peer {
    close() { assert.ok(stopping, "rate-limited settings must not disconnect playback"); }
  }
  for (let index = 0; index < 64; index++) {
    const notice = { textContent: "", hidden: true };
    notices.push(notice);
    runInNewContext(script, {
      location: { pathname: "/player/test" },
      document: { body: {}, getElementById: id => id === "v" ? { play: async () => {} }
        : id === "notice" ? notice : { textContent: "Connecting" } },
      RTCPeerConnection: WatchedPeer, AbortSignal, Date: { now: () => now },
      Math: Object.assign(Object.create(Math), { random: () => ((seed = (seed * 16807) % 2147483647) / 2147483647) }),
      fetch: async path => {
        if (path.endsWith("/playback") && limited) {
          assert.ok(now >= blockedUntil[index], "Retry-After must be respected");
          if (tokens < 1) {
            denials++;
            blockedUntil[index] = now + 2000;
            if (now > 180000) lateDenials++;
            return { ok: false, status: 429, headers: { get: () => "2" } };
          }
          tokens--;
          lastRead[index] = now;
        }
        return { ok: true, json: async () => ({ dynamicDelay: false }),
          headers: { get: () => "/whep/resource/test" }, text: async () => "" };
      },
      addEventListener: (event, callback) => { if (event === "pagehide") hide.push(callback); },
      setTimeout: (callback, delay) => { const timer = { callback, at: now + delay }; timers.push(timer); return timer; },
      clearTimeout: timer => { const index = timers.indexOf(timer); if (index >= 0) timers.splice(index, 1); },
    });
  }
  await new Promise(resolve => setImmediate(resolve));
  limited = true;
  // The production guard allows 10 requests/s with a burst of 20 per source IP.
  while (now < 300000) {
    timers.sort((a, b) => a.at - b.at);
    const next = timers.shift();
    assert.ok(next, "settings must keep refreshing");
    tokens = Math.min(20, tokens + (next.at - now) / 100);
    now = next.at;
    await next.callback();
  }
  assert.ok(denials > 0, "the fixture must exercise rate limiting");
  assert.equal(lateDenials, 0, "polls must settle below the shared allowance");
  assert.ok(lastRead.every(time => now - time < 40000), "every viewer must keep receiving settings");
  assert.ok(notices.every(notice => notice.hidden), "temporary rate limits must not cover the video");
  stopping = true;
  hide.forEach(callback => callback());
  assert.equal(timers.length, 0, "closing the pages must stop settings polling");
});

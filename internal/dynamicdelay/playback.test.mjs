// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

import assert from "node:assert/strict";
import test from "node:test";
import { rtpClock, clocksChanged, opusDuration, playbackRate, videoCodec, opusFragment, opusInit, renderVideo, supported } from "./web/player.js";

function mockGlobals(t, globals) {
  for (const [name, value] of Object.entries(globals)) {
    const saved = Object.getOwnPropertyDescriptor(globalThis, name);
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
    t.after(() => saved ? Object.defineProperty(globalThis, name, saved) : delete globalThis[name]);
  }
}

async function startWorker(t, clock) {
  class WorkerScope {}
  const scope = new WorkerScope(), messages = [], forwarded = [];
  mockGlobals(t, { WorkerGlobalScope: WorkerScope, self: scope, postMessage: message => messages.push(message) });
  await import(`./web/player.js?test=${encodeURIComponent(t.name)}`);
  const input = new TransformStream();
  scope.onmessage({ data: {
    options: { clock: { timestamp: 0, referenceMs: 0, ...clock }, originMS: 0 },
    readable: input.readable, writable: new WritableStream({ write: frame => forwarded.push(frame) }),
  } });
  return { scope, messages, forwarded, writer: input.writable.getWriter() };
}

test("clock comparison tolerates a day of RTP wraps while detecting accumulated drift", () => {
  const before = [90000, 48000].map((rate, idx) => ({ kind: idx ? "audio" : "video", rate,
    timestamp: 0xfffffff0, referenceMs: 4e12 }));
  for (let seconds = 60; seconds <= 86400; seconds += 60) {
    const next = before.map(clock => ({ ...clock, timestamp: (clock.timestamp + seconds * clock.rate) >>> 0,
      referenceMs: clock.referenceMs + seconds * 1000 }));
    assert.equal(clocksChanged(before, next), false);
    next[1].referenceMs += 600;
    assert.equal(clocksChanged(before, next), true);
  }
});

test("independent relay clock recovery refreshes A/V references without reacting to RTP wrap", () => {
  const before = [{ kind: "video", rate: 90000, timestamp: 0xffffff00, referenceMs: 1000 },
    { kind: "audio", rate: 48000, timestamp: 12345, referenceMs: 1020 }];
  const next = before.map(clock => ({ ...clock, timestamp: (clock.timestamp + clock.rate) >>> 0,
    referenceMs: clock.referenceMs + 1000 }));
  assert.equal(clocksChanged(before, next), false);
  for (const clock of next) clock.referenceMs += 3000;
  assert.equal(clocksChanged(before, next), false, "a common shift preserves A/V alignment");
  next[1].referenceMs -= 2095.41;
  assert.equal(clocksChanged(before, next), true, "the captured audio re-anchor must refresh playback");
  assert.equal(clocksChanged(next, next), false, "a fresh player must not reconnect repeatedly");
});

test("shared backward resets refresh both clocks after an hour of playback", () => {
  const attached = [90000, 48000].map((rate, index) => ({ kind: index ? "audio" : "video", rate,
    timestamp: 0xfffffff0, referenceMs: 4e12, epoch: 0 }));
  const recent = attached.map(clock => ({ ...clock,
    timestamp: (clock.timestamp + 3600 * clock.rate) >>> 0, referenceMs: clock.referenceMs + 3600000 }));
  assert.equal(clocksChanged(attached, recent), false);
  const reset = recent.map((clock, index) => ({ ...clock,
    timestamp: (attached[index].timestamp + clock.rate) >>> 0, referenceMs: clock.referenceMs + 1000, epoch: 1 }));
  assert.equal(clocksChanged(attached, reset), true,
    "equal resets must reconnect even when the new timestamps are ahead of the attachment snapshot");
  assert.equal(clocksChanged(reset, reset), false);
  for (const clock of reset) {
    const timestamp = rtpClock(clock, reset[0].referenceMs);
    assert.equal(timestamp(clock.timestamp), 0, "the replacement worker must accept the reset media");
  }
  const reordered = recent.map(clock => ({ ...clock,
    timestamp: (clock.timestamp - clock.rate) >>> 0, referenceMs: clock.referenceMs - 1000 }));
  assert.equal(clocksChanged(attached, reordered), false,
    "old packets retain their clock mapping and must not look like a sender reset");
  const shifted = recent.map(clock => ({ ...clock, referenceMs: clock.referenceMs + 3000 }));
  assert.equal(clocksChanged(attached, shifted), false);
  const reorderedAfterShift = shifted.map(clock => ({ ...clock,
    timestamp: (clock.timestamp - clock.rate) >>> 0, referenceMs: clock.referenceMs - 1000 }));
  assert.equal(clocksChanged(attached, reorderedAfterShift), false,
    "an accepted common clock correction must not make later reordered packets look like a reset");
});

test("video worker orders retransmitted frames before decoding and forwards every frame to WebRTC", async t => {
  const decoded = [], decoders = [];
  mockGlobals(t, {
    VideoDecoder: class {
      constructor(callbacks) { this.callbacks = callbacks; decoders.push(this); }
      state = "unconfigured";
      decodeQueueSize = 0;
      configure() { this.state = "configured"; }
      reset() { this.state = "unconfigured"; }
      addEventListener() {}
      decode(chunk) { decoded.push(chunk.timestamp); }
    },
    EncodedVideoChunk: class {
      constructor(options) { Object.assign(this, options); this.byteLength = options.data.length; }
    },
  });
  const { scope, messages, forwarded, writer } = await startWorker(t, { kind: "video", mime: "video/H264", rate: 90000 });
  const send = (stamp, type = "delta") => writer.write({ type,
    data: new Uint8Array([0,0,0,1,0x67,0x42,0xe0,0x28,0]).buffer,
    getMetadata: () => ({ rtpTimestamp: stamp }),
  });
  await send(0, "key");
  // Captured failure: a delta arrived before its keyframe, then several duplicates.
  await send(783000);
  await send(765000, "key");
  for (const stamp of [768000,771000,774000,777000,780000,768000,771000,774000,777000]) await send(stamp);
  scope.onmessage({ data: { horizon: 9 } });
  assert.deepEqual(decoded, [0,8500000,8533333,8566667,8600000,8633333,8666667,8700000]);
  await send(768000);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(decoded.length, 8, "an already decoded frame must not be decoded again");
  assert.equal(forwarded.length, 13, "native feedback still receives every frame");
  assert.deepEqual(messages, []);

  // A real decoder closes on a damaged frame; recovery must start with a keyframe.
  decoders[0].state = "closed";
  decoders[0].callbacks.error(new Error("Decoding error"));
  assert.deepEqual(messages, [], "one damaged frame must not disable smooth playback");
  scope.onmessage({ data: { horizon: 20 } });
  await send(900000);
  assert.equal(decoded.length, 8, "wait for a keyframe after losing decoder references");
  await send(903000, "key");
  assert.equal(decoders.length, 2);
  assert.equal(decoded.at(-1), 10033333);
  const recovered = { timestamp: 10033333 };
  decoders.at(-1).callbacks.output(recovered);
  assert.deepEqual(messages.pop(), { frame: recovered });
  for (let attempt = 0; attempt < 3; attempt++) {
    const current = decoders.at(-1);
    current.state = "closed";
    current.callbacks.error(new Error("Decoding error"));
    if (attempt < 2) {
      assert.deepEqual(messages, [], "successful output resets the failure count");
      await send(906000 + attempt * 3000, "key");
    }
  }
  assert.deepEqual(messages, [{ error: "Cannot decode video" }], "repeated decoder failure still falls back");
  await writer.close();
});

test("late joining preserves A/V offset across random RTP origins and wraps", () => {
  const video = rtpClock({ timestamp: 0xfffffff0, rate: 90000, referenceMs: 2000 }, 1900);
  const audio = rtpClock({ timestamp: 871239, rate: 48000, referenceMs: 2020 }, 1900);
  assert.equal(video(2984), 12000);
  assert.equal(audio(872199), 6720);
  assert.equal(video(2984), 12000);
  assert.equal(video(2983), 11999);
  assert.equal(video(5984), 15000);
  // Multiple wraps work because the snapshot is only used to establish the clock.
  let value = 5984, elapsed = 15000;
  for (let idx = 0; idx < 100; idx++) {
    value = (value + 90000000) >>> 0;
    elapsed += 90000000;
    assert.equal(video(value), elapsed);
  }
});

test("lost Opus packets preserve audio duration without replaying late duplicates", async t => {
  const { messages, forwarded, writer } = await startWorker(t, { kind: "audio", mime: "audio/opus", rate: 48000 });
  const stamps = [0,960,2881,3840,4800,2880,5760,6960,7920,100000,100960,101920,102880,103840,
    105280,106360,107320,108280,109240,158200];
  for (const stamp of stamps) await writer.write({ data: new Uint8Array([0xf8,0xff,0xfe]).buffer,
    getMetadata: () => ({ rtpTimestamp: stamp }) });
  await writer.close();
  const fragments = messages.filter(message => message.data).slice(1).map(({ data }) => {
    const bytes = new Uint8Array(data), view = new DataView(data);
    const [moof, mdat] = boxes(bytes);
    const traf = boxes(bytes, moof.start, moof.end).find(box => box.type === "traf");
    const children = boxes(bytes, traf.start, traf.end);
    const tfdt = children.find(box => box.type === "tfdt").start;
    const trun = children.find(box => box.type === "trun").start;
    const durations = [];
    let payload = mdat.start;
    for (let index = 0; index < view.getUint32(trun + 4); index++) {
      const entry = trun + 12 + index * 12;
      const duration = view.getUint32(entry), size = view.getUint32(entry + 4);
      assert.equal(opusDuration(bytes.subarray(payload, payload + size)), duration);
      durations.push(duration); payload += size;
    }
    return { time: Number(view.getBigUint64(tfdt + 4)), durations };
  });
  assert.deepEqual(fragments, [
    { time: 0, durations: [960,960,960,960,960] },
    { time: 4800, durations: [960,960,240,960,960] },
    { time: 100000, durations: [960,960,960,960,960] },
    { time: 104800, durations: [480,960,120,960,960] },
    { time: 108280, durations: Array(53).fill(960) },
  ], "fill short holes and ignore one-tick jitter; preserve large gaps for seek recovery");
  assert.equal(forwarded.length, stamps.length, "native WebRTC still receives every packet for feedback");
});

test("Opus durations follow the TOC for variable packet sizes", () => {
  assert.equal(opusDuration(new Uint8Array([16 << 3])), 120);
  assert.equal(opusDuration(new Uint8Array([19 << 3])), 960);
  assert.equal(opusDuration(new Uint8Array([(19 << 3) | 1])), 1920);
  assert.equal(opusDuration(new Uint8Array([(19 << 3) | 3, 6])), 5760);
  assert.equal(opusDuration(new Uint8Array([3 << 3])), 2880);
  assert.throws(() => opusDuration(new Uint8Array()));
  assert.throws(() => opusDuration(new Uint8Array([(19 << 3) | 3])));
  assert.throws(() => opusDuration(new Uint8Array([(19 << 3) | 3, 7])));
});

test("speed is bounded and returns to normal as backlog drains", () => {
  for (const speed of [1, 10, 20, 50, 100, 200]) {
    assert.equal(playbackRate(10, { autoCatchUp: false, catchUpMsPerSecond: speed }), 1 + speed / 1000);
    assert.equal(playbackRate(0.5, { autoCatchUp: false, catchUpMsPerSecond: speed }), 1);
    assert.equal(playbackRate(0, { autoCatchUp: false, catchUpMsPerSecond: speed }), 0.95);
    assert.ok(playbackRate(0.501, { autoCatchUp: false, catchUpMsPerSecond: speed }) <= 1 + speed / 1000);
    assert.equal(playbackRate(0.6, { autoCatchUp: false, catchUpMsPerSecond: speed }, 1 + speed / 1000), 1 + speed / 1000);
    assert.equal(playbackRate(0.6, { autoCatchUp: false, catchUpMsPerSecond: speed }, 1), 1);
    assert.equal(playbackRate(0.39, { autoCatchUp: false, catchUpMsPerSecond: speed }, 1 + speed / 1000), 1);
    assert.equal(playbackRate(0.4, { autoCatchUp: false, catchUpMsPerSecond: speed }, 0.95), 0.95);
    assert.equal(playbackRate(0.51, { autoCatchUp: false, catchUpMsPerSecond: speed }, 0.95), 1);
  }
});

test("automatic speed follows excess delay, caps near maximum and keeps buffer hysteresis", () => {
  const settings = { delayMs: 2000, currentDelayMs: 2000, maxDelayMs: 10000, catchUpMsPerSecond: 200 };
  assert.equal(playbackRate(0.6, settings, 1.01), 1.01);
  assert.equal(playbackRate(0.5, settings), 1);
  assert.equal(playbackRate(0.2, settings, 1.2), 0.95);
  let previous = 1.01;
  for (let delay = 2000; delay <= 12000; delay += 100) {
    settings.currentDelayMs = delay;
    const rate = playbackRate(0.6, settings, previous);
    assert.ok(rate >= previous && rate <= 1.2);
    previous = rate;
  }
  settings.currentDelayMs = 9000;
  assert.equal(playbackRate(0.8, settings), 1.2);
  assert.equal(playbackRate(0.5, settings), 1, "relay backlog alone must not drain a healthy player buffer");
  settings.currentDelayMs = 2000;
  assert.equal(playbackRate(8, settings), 1.2, "player backlog contributes to the same delay limit");
  settings.maxDelayMs = 2000;
  assert.equal(playbackRate(1, settings), 1.2, "equal normal and maximum delay cannot divide by zero");
  settings.autoCatchUp = false;
  settings.catchUpMsPerSecond = 50;
  assert.equal(playbackRate(8, settings), 1.05, "manual mode ignores automatic scaling");
});

function boxes(data, from = 0, end = data.length) {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const found = [];
  for (let pos = from; pos < end;) {
    const length = view.getUint32(pos);
    assert.ok(length >= 8 && pos + length <= end);
    found.push({ type: String.fromCharCode(...data.subarray(pos + 4, pos + 8)), start: pos + 8, end: pos + length });
    pos += length;
  }
  return found;
}

test("H264 profile extraction accepts either Annex B start code", () => {
  const annex = new Uint8Array([0,0,0,1,0x67,0x42,0xe0,0x28,0,0,1,0x68,1,2,0,0,0,1,0x65,3,4,5]);
  assert.equal(videoCodec(annex, "video/h264"), "avc1.42e028");
  assert.equal(videoCodec(new Uint8Array([0,0,1,0x65,1]), "video/h264"), null);
  assert.equal(videoCodec(new Uint8Array([0,0,1,0x67,0x42]), "video/h264"), null);
  assert.deepEqual(boxes(opusInit()).map(box => box.type), ["ftyp", "moov"]);
});

test("H265 profile extraction removes emulation bytes and rejects truncated SPS", () => {
  const sps = Buffer.from("00000001420103016000000300b00000030000030078", "hex");
  assert.equal(videoCodec(sps, "video/H265"), "hvc1.1.6.L120.b0");
  assert.equal(videoCodec(sps.subarray(1), "video/H265"), "hvc1.1.6.L120.b0");
  assert.equal(videoCodec(sps.subarray(0, -1), "video/H265"), null);
  assert.equal(videoCodec(Buffer.concat([sps.subarray(0, 10), Buffer.from("0000014401ffffffffff", "hex")]), "video/H265"), null);
  const high = Buffer.from("000001420103620000030003800000030000030093", "hex");
  assert.equal(videoCodec(high, "video/H265"), "hvc1.A2.c0000000.H147.80");
});

test("AV1 reads sequence headers after delimiters and rejects malformed OBUs", () => {
  const sequence = Buffer.from("0a0e00000042abbfc374068808080820", "hex");
  assert.equal(videoCodec(sequence, "video/AV1"), "av01.0.08M.08");
  assert.equal(videoCodec(Buffer.concat([Buffer.from([0x12, 0]), sequence]), "video/AV1"), "av01.0.08M.08");
  const extended = Buffer.concat([Buffer.from([0x0e, 0]), sequence.subarray(1)]);
  assert.equal(videoCodec(extended, "video/AV1"), "av01.0.08M.08");
  for (const data of [sequence.subarray(0, -1), Buffer.from([0x0a, 0xff]),
    Buffer.from([0x8a, 0]), Buffer.from([0x08, 0]), Buffer.from([0x0a, ...Array(8).fill(0x80)]),
    Buffer.from([0x12, 0])]) assert.equal(videoCodec(data, "video/AV1"), null);
  assert.throws(() => videoCodec(Buffer.from([0x0a, 1, 0]), "video/AV1"), /Truncated/);
});

test("AV1 reads bit depth, reduced headers and optional timing across operating points", () => {
  for (const [hex, codec] of [
    ["0a051a00001000", "av01.0.08M.08"],
    ["0a051a00003000", "av01.0.08M.10"],
    ["0a055a00003800", "av01.2.08M.12"],
    // Two operating points, timing/decoder-model/display-delay fields, frame IDs
    // and screen-content flags precede the 10-bit color configuration.
    ["0a1d44000000040000007a5880000000400840011808000900020004502800", "av01.2.08H.10"],
  ]) assert.equal(videoCodec(Buffer.from(hex, "hex"), "video/AV1"), codec);
});

test("smooth playback accepts H264/H265/AV1 with Opus and falls back without decoder APIs", t => {
  mockGlobals(t, { MediaSource: { isTypeSupported: () => true },
    Worker: class {}, VideoDecoder: class {}, RTCRtpScriptTransform: class {} });
  const clocks = [{ kind: "audio", mime: "audio/opus", rate: 48000 },
    { kind: "video", mime: "video/H265", rate: 90000 }];
  assert.equal(supported({ clocks }), true);
  clocks[1].mime = "video/H264";
  assert.equal(supported({ clocks }), true);
  clocks[1].mime = "video/AV1";
  assert.equal(supported({ clocks }), true);
  clocks[1].mime = "video/VP9";
  assert.equal(supported({ clocks }), false);
  clocks[1].mime = "video/AV1";
  assert.equal(supported({ clocks: clocks.slice(1) }), false);
  globalThis.VideoDecoder = undefined;
  assert.equal(supported({ clocks }), false);
});

test("fragments retain 64-bit timestamps, sample durations and payload offsets", () => {
  const data = opusFragment(7, 0x100000012, [
    { duration: 3000, data: new Uint8Array([1,2,3]) },
    { duration: 3001, data: new Uint8Array([4,5]) },
  ]);
  const view = new DataView(data.buffer);
  const [moof, mdat] = boxes(data);
  const traf = boxes(data, moof.start, moof.end).find(box => box.type === "traf");
  const children = boxes(data, traf.start, traf.end);
  const tfdt = children.find(box => box.type === "tfdt").start;
  assert.equal(view.getBigUint64(tfdt + 4), 0x100000012n);
  const trun = children.find(box => box.type === "trun").start;
  assert.equal(view.getUint32(trun + 4), 2);
  assert.equal(view.getUint32(trun + 8), mdat.start);
  assert.equal(view.getUint32(trun + 12), 3000);
  assert.equal(view.getUint32(trun + 24), 3001);
  assert.deepEqual(data.subarray(mdat.start), new Uint8Array([1,2,3,4,5]));
});

test("video keeps cadence through audio clock jitter and releases frames on seek/close", t => {
  let next, removed = false, made = 0, closed = 0;
  const shown = [];
  const audio = { paused: false, seeking: false, readyState: 4, currentTime: 0, playbackRate: 1, after() {} };
  const canvas = { style: {}, setAttribute() {}, remove() { removed = true; },
    getContext: () => ({ drawImage: frame => shown.push({ time: frame.timestamp / 1e6, audio: audio.currentTime }) }) };
  mockGlobals(t, {
    document: { createElement: () => canvas },
    requestAnimationFrame: callback => { next = callback; return 1; },
    cancelAnimationFrame: () => { next = null; },
  });
  const renderer = renderVideo(audio);
  const frame = time => {
    made++;
    let released = false;
    return { timestamp: time * 1e6, displayWidth: 1920, displayHeight: 1080,
      close() { assert.equal(released, false); released = true; closed++; } };
  };
  let time = 0, index = 0;
  for (let tick = 0; tick < 600; tick++) {
    audio.playbackRate = tick >= 180 && tick < 420 ? 1.1 : 1;
    if (tick) time += audio.playbackRate / 60;
    audio.currentTime = time + Math.sin(tick * 1.7) * 0.02;
    while (index / 30 < time + 0.4) renderer.push(frame(index++ / 30));
    next(tick * 1000 / 60);
  }
  assert.equal(renderer.stats.skipped, 0);
  assert.equal(renderer.stats.freezes, 0);
  assert.ok(renderer.stats.rendered > 300);
  assert.ok(shown.every(item => Math.abs(item.time - item.audio) < 0.07));
  audio.currentTime = 20;
  renderer.push(frame(20));
  next(20000);
  audio.currentTime = 20.033;
  renderer.push(frame(20.033));
  renderer.push(frame(18.5));
  next(20033);
  assert.equal(shown.at(-1).time, 20.033, "late decoder output must not replace a newer frame");
  const rendered = shown.length;
  renderer.push(frame(19));
  next(20050);
  assert.equal(shown.length, rendered, "stale frames must not rewind the picture behind audio");
  audio.paused = true;
  audio.currentTime = 20;
  renderer.push(frame(15));
  renderer.push(frame(20));
  renderer.close();
  assert.equal(made, closed);
  assert.equal(next, null);
  assert.equal(removed, true);
});

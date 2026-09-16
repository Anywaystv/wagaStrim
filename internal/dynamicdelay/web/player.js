// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Unwrap from a recent relay snapshot, then accumulate so long streams can wrap.
export function rtpClock(clock, originMS) {
  let previous = clock.timestamp;
  let ticks = Math.round((clock.referenceMs - originMS) * clock.rate / 1000);
  return timestamp => {
    ticks += (timestamp - previous) | 0;
    previous = timestamp;
    return ticks;
  };
}

// Compare with the attached player's clocks so small changes cannot accumulate unnoticed.
export function clocksChanged(previous, next) {
  if (next.some(clock => clock.epoch !== previous.find(track => track.kind === clock.kind).epoch)) return true;
  const shifts = next.map(clock => {
    const before = previous.find(track => track.kind === clock.kind);
    const shift = clock.referenceMs - before.referenceMs
      - ((clock.timestamp - before.timestamp) | 0) * 1000 / clock.rate;
    const wrapMS = 4294967296 * 1000 / clock.rate;
    return shift - Math.round(shift / wrapMS) * wrapMS;
  });
  return Math.max(...shifts) - Math.min(...shifts) > 100;
}

// Opus frames are not necessarily 20 ms. Gaps must stay gaps, not stretched audio.
export function opusDuration(data) {
  if (!data.length) throw new Error("Empty Opus packet");
  const config = data[0] >> 3;
  const frame = config >= 16 ? 120 << (config & 3)
    : config >= 12 ? 480 << (config & 1) : [480, 960, 1920, 2880][config & 3];
  const code = data[0] & 3;
  const count = code === 0 ? 1 : code === 3 ? (data[1] & 63) : 2;
  if (!count || frame * count > 5760) throw new Error("Invalid Opus duration");
  return frame * count;
}

export function playbackRate(ahead, settings, previous = 1) {
  let speed = settings.catchUpMsPerSecond;
  if (settings.autoCatchUp !== false) {
    const extra = Math.max(0, settings.currentDelayMs - settings.delayMs) + Math.max(0, ahead - 0.5) * 1000;
    const span = Math.max(1, settings.maxDelayMs - settings.delayMs);
    speed = Math.min(200, 10 + 190 * extra / (0.9 * span));
    speed = Math.round(speed / 10) * 10;
  }
  // Keep a steady rate through 100 ms fragment batches. Repeated rate changes
  // reset Chromium's audio/video scheduling and can discard decoded frames.
  if (ahead > 0.65 || (previous > 1 && ahead > 0.4)) return 1 + speed / 1000;
  if (ahead < 0.3 || (previous < 1 && ahead < 0.5)) return 0.95;
  return 1;
}

const bytes = (...parts) => {
  const result = new Uint8Array(parts.reduce((size, part) => size + part.length, 0));
  let offset = 0;
  for (const part of parts) { result.set(part, offset); offset += part.length; }
  return result;
};
const zeros = size => new Uint8Array(size);
const str = text => Uint8Array.from(text, char => char.charCodeAt(0));
const u16 = value => new Uint8Array([value >>> 8, value]);
const u32 = value => new Uint8Array([value >>> 24, value >>> 16, value >>> 8, value]);
const box = (name, ...parts) => {
  const data = bytes(...parts);
  return bytes(u32(data.length + 8), str(name), data);
};
const full = (name, version, flags, ...parts) =>
  box(name, new Uint8Array([version, flags >>> 16, flags >>> 8, flags]), ...parts);

// Read the sequence header only when configuring the decoder, not for each frame.
function av1Codec(data) {
  for (let pos = 0; pos < data.length;) {
    const header = data[pos++];
    if ((header & 0x81) || !(header & 2)) return null;
    if (header & 4) pos++;
    let size = 0, shift = 0, byte;
    do {
      if (pos >= data.length || shift >= 56) return null;
      byte = data[pos++];
      size += (byte & 127) * 2 ** shift;
      shift += 7;
    } while (byte & 128);
    const end = pos + size;
    if (end > data.length) return null;
    if (((header >> 3) & 15) !== 1) { pos = end; continue; }
    let bit = pos * 8;
    const read = count => {
      if (bit + count > end * 8) throw new Error("Truncated AV1 sequence header");
      let value = 0;
      while (count--) { value = value * 2 + ((data[bit >> 3] >> (7 - (bit & 7))) & 1); bit++; }
      return value;
    };
    const profile = read(3);
    if (profile > 2) return null;
    read(1); // still_picture
    const reduced = read(1);
    let level, tier = 0, bufferBits = 0;
    if (reduced) level = read(5);
    else {
      if (read(1)) { // timing_info_present_flag
        read(64);
        if (read(1)) {
          let zeros = 0;
          while (zeros < 32 && !read(1)) zeros++;
          if (zeros < 32) read(zeros);
        }
        if (read(1)) { bufferBits = read(5) + 1; read(42); }
      }
      const displayDelay = read(1), points = read(5) + 1;
      for (let point = 0; point < points; point++) {
        read(12); // operating_point_idc
        const nextLevel = read(5), nextTier = nextLevel > 7 ? read(1) : 0;
        if (point === 0) { level = nextLevel; tier = nextTier; }
        if (bufferBits && read(1)) read(2 * bufferBits + 1);
        if (displayDelay && read(1)) read(4);
      }
    }
    const widthBits = read(4) + 1, heightBits = read(4) + 1;
    read(widthBits + heightBits);
    if (!reduced && read(1)) read(7); // frame ID lengths
    read(3); // superblock and intra filtering flags
    if (!reduced) {
      read(4); // inter prediction flags
      const orderHint = read(1);
      if (orderHint) read(2);
      const screenTools = read(1) ? 2 : read(1);
      if (screenTools && !read(1)) read(1);
      if (orderHint) read(3);
    }
    read(3); // super-resolution, CDEF and restoration
    const highDepth = read(1);
    const depth = highDepth ? (profile === 2 && read(1) ? 12 : 10) : 8;
    return `av01.${profile}.${String(level).padStart(2, "0")}${tier ? "H" : "M"}.${String(depth).padStart(2, "0")}`;
  }
  return null;
}

// H264/H265 use Annex B; AV1 uses the low-overhead OBU format from WebRTC.
export function videoCodec(data, mime) {
  if (mime.toLowerCase() === "video/av1") return av1Codec(data);
  const hevc = mime.toLowerCase() === "video/h265";
  for (let idx = 0; idx + 6 < data.length; idx++) {
    if (data[idx] || data[idx + 1]) continue;
    const prefix = data[idx + 2] === 1 ? 3 : data[idx + 2] === 0 && data[idx + 3] === 1 ? 4 : 0;
    if (!prefix || idx + prefix + 3 >= data.length) continue;
    const nal = idx + prefix;
    if ((hevc ? (data[nal] >> 1) & 63 : data[nal] & 31) !== (hevc ? 33 : 7)) continue;
    const hex = value => value.toString(16).padStart(2, "0");
    if (!hevc) return "avc1." + Array.from(data.subarray(nal + 1, nal + 4), hex).join("");
    // The first SPS byte precedes profile_tier_level. Remove emulation prevention
    // bytes, stopping at the next NAL so a truncated SPS cannot borrow its bytes.
    const profile = [];
    for (let pos = nal + 2; pos < data.length && profile.length < 13; pos++) {
      if (pos > nal + 3 && data[pos - 2] === 0 && data[pos - 1] === 0) {
        if (data[pos] === 3) continue;
        if (data[pos] <= 1) break;
      }
      profile.push(data[pos]);
    }
    if (profile.length < 13) return null;
    let compatibility = 0;
    for (let bit = 0; bit < 32; bit++) {
      if (profile[2 + (bit >> 3)] & (128 >> (bit & 7))) compatibility += 2 ** bit;
    }
    const constraints = profile.slice(6, 12);
    while (constraints.at(-1) === 0) constraints.pop();
    return ["hvc1", ["", "A", "B", "C"][profile[1] >> 6] + (profile[1] & 31),
      compatibility.toString(16), (profile[1] & 32 ? "H" : "L") + profile[12],
      ...constraints.map(hex)].join(".");
  }
  return null;
}

export function opusInit() {
  const matrix = bytes(u32(0x10000), zeros(12), u32(0x10000), zeros(12), u32(0x40000000));
  const entry = box("Opus", zeros(6), u16(1), zeros(8), u16(2), u16(16), zeros(4), u32(48000 * 65536),
    box("dOps", new Uint8Array([0, 2]), u16(0), u32(48000), u16(0), new Uint8Array([0])));
  const stbl = box("stbl", full("stsd", 0, 0, u32(1), entry), full("stts", 0, 0, u32(0)),
    full("stsc", 0, 0, u32(0)), full("stsz", 0, 0, u32(0), u32(0)), full("stco", 0, 0, u32(0)));
  const dinf = box("dinf", full("dref", 0, 0, u32(1), full("url ", 0, 1)));
  const mdia = box("mdia", full("mdhd", 0, 0, zeros(8), u32(48000), u32(0), u16(0x55c4), u16(0)),
    full("hdlr", 0, 0, u32(0), str("soun"), zeros(12), str("waga\0")),
    box("minf", full("smhd", 0, 0, zeros(4)), dinf, stbl));
  const trak = box("trak", full("tkhd", 0, 7, zeros(8), u32(1), zeros(16), u16(0), u16(0),
    u16(256), u16(0), matrix, zeros(8)), mdia);
  const moov = box("moov", full("mvhd", 0, 0, zeros(8), u32(1000), u32(0), u32(65536),
    u16(256), zeros(10), matrix, zeros(24), u32(2)), trak,
    box("mvex", full("trex", 0, 0, u32(1), u32(1), u32(0), u32(0), u32(0))));
  return bytes(box("ftyp", str("isom"), u32(0x200), str("isomiso6mp41")), moov);
}

export function opusFragment(sequence, time, samples) {
  const make = offset => box("moof", full("mfhd", 0, 0, u32(sequence)),
    box("traf", full("tfhd", 0, 0x20000, u32(1)),
      full("tfdt", 1, 0, u32(Math.floor(time / 4294967296)), u32(time)),
      full("trun", 0, 0x701, u32(samples.length), u32(offset),
        ...samples.map(sample => bytes(u32(sample.duration), u32(sample.data.length), u32(0x02000000))))));
  const moof = make(0);
  return bytes(make(moof.length + 8), box("mdat", ...samples.map(sample => sample.data)));
}

// Video follows the audio clock without inheriting the media element's frame
// discards when its audio time-stretcher changes rate.
export function renderVideo(audio) {
  const canvas = document.createElement("canvas");
  canvas.style.cssText = "position:absolute;inset:0;width:100%;height:100%;object-fit:contain;pointer-events:none";
  canvas.setAttribute("aria-hidden", "true");
  audio.after(canvas);
  const context = canvas.getContext("2d", { alpha: false });
  const frames = [];
  const stats = { rendered: 0, skipped: 0, freezes: 0, maxGapMS: 0 };
  let request, last, position, painted;
  const draw = now => {
    request = requestAnimationFrame(draw);
    if (audio.paused || audio.readyState < 2) { last = now; return; }
    if (position === undefined || Math.abs(audio.currentTime - position) > 0.25) position = audio.currentTime;
    else {
      position += Math.min(0.1, (now - last) / 1000) * audio.playbackRate;
      // WSOLA consumption varies between audio callbacks. Follow its average
      // without allowing a sustained audio/video clock offset over 50 ms.
      position += Math.max(-0.001, Math.min(0.001, (audio.currentTime - position) * 0.05));
      position = Math.max(audio.currentTime - 0.05, Math.min(audio.currentTime + 0.05, position));
    }
    last = now;
    let frame;
    while (frames.length && frames[0].timestamp / 1e6 <= position + 0.008) {
      if (frame) { frame.close(); stats.skipped++; }
      frame = frames.shift();
    }
    if (!frame) return;
    // Recovery can deliver decoded frames whose presentation time has passed.
    if (frame.timestamp < (audio.currentTime - 0.1) * 1e6) { frame.close(); stats.skipped++; return; }
    if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
      canvas.width = frame.displayWidth;
      canvas.height = frame.displayHeight;
    }
    context.drawImage(frame, 0, 0);
    frame.close();
    if (painted !== undefined) {
      stats.maxGapMS = Math.max(stats.maxGapMS, now - painted);
      if (now - painted > 150) stats.freezes++;
    }
    painted = now;
    stats.rendered++;
  };
  request = requestAnimationFrame(draw);
  return {
    stats,
    push(frame) {
      // A seek can advance audio while paused. Release old decoded frames
      // without waiting for playback to resume.
      if (audio.paused || audio.seeking) {
        while (frames.length && frames[0].timestamp < (audio.currentTime - 0.1) * 1e6) {
          frames.shift().close(); stats.skipped++;
        }
        if (frame.timestamp < (audio.currentTime - 0.1) * 1e6) { frame.close(); stats.skipped++; return; }
      }
      if (frames.length >= 90) { frame.close(); throw new Error("Video renderer cannot keep up"); }
      if (frames.length && frame.timestamp < frames.at(-1).timestamp) {
        const index = frames.findIndex(queued => queued.timestamp > frame.timestamp);
        frames.splice(index, 0, frame);
      } else frames.push(frame);
    },
    close() {
      cancelAnimationFrame(request);
      for (const frame of frames) frame.close();
      frames.length = 0;
      canvas.remove();
    },
  };
}

export function supported(settings) {
  const tracks = settings.clocks;
  return typeof MediaSource !== "undefined" && typeof Worker !== "undefined"
    && typeof VideoDecoder !== "undefined"
    && (typeof RTCRtpScriptTransform !== "undefined" || "createEncodedStreams" in RTCRtpReceiver.prototype)
    && tracks?.length === 2 && new Set(tracks.map(track => track.kind)).size === 2 && tracks.every(track =>
      (["video/h264", "video/h265", "video/av1"].includes(track.mime.toLowerCase()) && track.rate === 90000)
      || (track.mime.toLowerCase() === "audio/opus" && track.rate === 48000))
    && MediaSource.isTypeSupported('audio/mp4; codecs="opus"');
}

export function createPlayer(video, settings, fail, play) {
  const source = new MediaSource();
  const url = URL.createObjectURL(source);
  const worker = new Worker(import.meta.url, { type: "module" });
  const queue = [];
  let buffer;
  const renderer = renderVideo(video);
  const originMS = Math.min(...settings.clocks.map(track => track.referenceMs));
  let stopped = false, begun = false, lastRateChange = 0;
  const autoplay = video.autoplay;
  video.autoplay = false;
  video.pause();
  video.srcObject = null;
  video.src = url;
  video.preservesPitch = true;
  const failed = () => { if (!stopped) fail("Smooth playback failed. Using standard playback."); };
  const startup = setTimeout(failed, 20000);
  const drain = () => {
    if (stopped || !buffer || buffer.updating) return;
    try {
      const old = video.currentTime - 3;
      if (old > 0 && buffer.buffered.length && buffer.buffered.start(0) < old - 1) {
        buffer.remove(0, old);
      } else if (queue.length) {
        const data = queue.shift();
        buffer.appendBuffer(data);
        worker.postMessage({ ack: data.byteLength });
      }
    } catch { failed(); }
  };
  worker.onerror = event => { event.preventDefault(); if (!stopped) fail("Reconnecting", true); };
  video.addEventListener("error", failed);
  worker.onmessage = ({ data }) => {
    if (stopped) { data.frame?.close(); return; }
    if (data.error) return failed();
    try {
      if (data.frame) { renderer.push(data.frame); return; }
      if (data.mime) {
        if (!MediaSource.isTypeSupported(data.mime)) return failed();
        buffer = source.addSourceBuffer(data.mime);
        buffer.addEventListener("updateend", drain);
        buffer.addEventListener("error", failed);
      } else queue.push(data.data);
      drain();
    } catch { failed(); }
  };
  const opened = new Promise(resolve => source.addEventListener("sourceopen", resolve, { once: true }));
  const timer = setInterval(() => {
    const ranges = video.buffered;
    if (!ranges.length) return;
    const end = ranges.end(ranges.length - 1);
    if (!begun) {
      if (end - ranges.start(ranges.length - 1) < 0.5) return;
      video.currentTime = Math.max(ranges.start(ranges.length - 1), end - 0.5);
      begun = true;
      clearTimeout(startup);
      play();
    }
    // A relay keyframe skip leaves a real gap in RTP time. Cross it on both
    // tracks together, only after the media element has drained its old range.
    for (let idx = 0; idx < ranges.length; idx++) {
      if (video.currentTime < ranges.start(idx) - 0.01 && (video.readyState < 3 || video.currentTime < ranges.start(0))) {
        video.currentTime = ranges.start(idx);
        break;
      }
    }
    let ahead = end - video.currentTime;
    if (settings.jumpAtMaximum && ahead > Math.max(0.5, settings.maxDelayMs / 1000)) {
      video.currentTime = Math.max(ranges.start(ranges.length - 1), end - 0.5);
      ahead = end - video.currentTime;
    }
    const nextRate = playbackRate(ahead, settings, video.playbackRate);
    const now = performance.now();
    if (nextRate !== video.playbackRate &&
        (nextRate <= 1 || video.playbackRate <= 1 || now - lastRateChange >= 1000)) {
      video.playbackRate = nextRate;
      lastRateChange = now;
    }
    worker.postMessage({ horizon: video.currentTime + 0.5 });
  }, 100);

  return {
    stats: renderer.stats,
    async attach(receivers) {
      await opened;
      if (stopped) return;
      for (const clock of settings.clocks) {
        const receiver = receivers.find(item => item.track?.kind === clock.kind);
        if (!receiver) throw new Error("Missing playback track");
        const options = { clock, originMS };
        if (typeof RTCRtpScriptTransform !== "undefined") {
          receiver.transform = new RTCRtpScriptTransform(worker, options);
        } else {
          const { readable, writable } = receiver.createEncodedStreams();
          worker.postMessage({ readable, writable, options }, [readable, writable]);
        }
      }
    },
    update(next) { settings = next; },
    close() {
      stopped = true;
      clearInterval(timer);
      clearTimeout(startup);
      worker.terminate();
      renderer.close();
      video.removeEventListener("error", failed);
      video.pause();
      video.removeAttribute("src");
      video.load();
      video.playbackRate = 1;
      video.autoplay = autoplay;
      URL.revokeObjectURL(url);
      queue.length = 0;
    },
  };
}

let inFlight = 0, horizon = 0.5, decodeVideo;

function receive({ readable, writable, options }) {
  const { clock, originMS } = options;
  const { kind, rate } = clock;
  const timestamp = rtpClock(clock, originMS);
  let config, sequence = 0, samples = [], compressed = 0, decodedTime = -1, audioEnd;
  const chunks = [];
  const send = data => {
    inFlight += data.byteLength;
    if (inFlight > 32 * 1024 * 1024) throw new Error("Playback cannot keep up");
    postMessage({ data: data.buffer }, [data.buffer]);
  };
  const flush = () => {
    send(opusFragment(++sequence, samples[0].time, samples));
    samples = [];
  };
  let decoder;
  if (kind === "video") {
    let errors = 0;
    const createDecoder = () => {
      const next = new VideoDecoder({
        output: frame => { errors = 0; postMessage({ frame }, [frame]); },
        error: () => {
          if (++errors >= 3) postMessage({ error: "Cannot decode video" });
          else decodeVideo();
        },
      });
      next.addEventListener("dequeue", () => decodeVideo());
      return next;
    };
    decoder = createDecoder();
    decodeVideo = () => {
      if (decoder.state === "closed") {
        const key = chunks.findIndex(chunk => chunk.type === "key");
        if (key < 0 || errors >= 3) return;
        // A failed decoder cannot be reset. Resume with a fresh keyframe.
        for (const chunk of chunks.splice(0, key)) compressed -= chunk.byteLength;
        decoder = createDecoder();
        decoder.configure(config);
      }
      if (decoder.state !== "configured") return;
      if (chunks.length && chunks[0].timestamp < (horizon - 1) * 1e6) {
        const key = chunks.findLastIndex(chunk => chunk.type === "key" && chunk.timestamp <= (horizon - 0.5) * 1e6);
        if (key > 0) {
          for (const chunk of chunks.splice(0, key)) compressed -= chunk.byteLength;
          decoder.reset();
          decoder.configure(config);
        }
      }
      // Reserve 200 ms of the existing player buffer for reordered frames.
      while (chunks.length && chunks[0].timestamp <= (horizon - 0.2) * 1e6 && decoder.decodeQueueSize < 4) {
        const chunk = chunks.shift();
        compressed -= chunk.byteLength;
        decoder.decode(chunk);
        decodedTime = chunk.timestamp;
      }
    };
  }
  readable.pipeThrough(new TransformStream({
    transform(frame, controller) {
      const meta = frame.getMetadata();
      const time = timestamp(meta.rtpTimestamp ?? frame.timestamp);
      const raw = new Uint8Array(frame.data);
      if (raw.length > 8 * 1024 * 1024) throw new Error("Encoded frame too large");
      if (kind === "video") {
        if (!config && frame.type === "key") {
          const codec = videoCodec(raw, clock.mime);
          if (!codec) throw new Error("Video keyframe has no sequence header");
          config = { codec, optimizeForLatency: true };
          decoder.configure(config);
        }
        if (config && time >= 0) {
          const chunk = new EncodedVideoChunk({ type: frame.type, timestamp: Math.round(time * 1e6 / rate), data: raw });
          // Encoded transforms can deliver retransmitted frames out of order.
          const index = chunks.findIndex(queued => queued.timestamp >= chunk.timestamp);
          if (chunk.timestamp > decodedTime && (index < 0 || chunks[index].timestamp !== chunk.timestamp)) {
            compressed += chunk.byteLength;
            if (compressed > 32 * 1024 * 1024) throw new Error("Video buffer full");
            if (index < 0) chunks.push(chunk);
            else chunks.splice(index, 0, chunk);
            decodeVideo();
          }
        }
      } else {
        if (!config) {
          config = true;
          postMessage({ mime: 'audio/mp4; codecs="opus"' });
          send(opusInit());
        }
        if (time >= 0) {
          if (time <= audioEnd - 120) { controller.enqueue(frame); return; }
          const item = { time, data: raw.slice(), duration: opusDuration(raw) };
          // MSE can collapse short Opus gaps into continuous audio. Fill lost
          // time with 2.5 to 20 ms silence packets; large gaps still use seek recovery.
          let gap = time - audioEnd;
          while (gap >= 120 && gap <= rate) {
            const size = Math.min(3, Math.floor(Math.log2(gap / 120)));
            const duration = 120 << size;
            samples.push({ time: audioEnd, duration, data: new Uint8Array([0xe0 + size * 8, 0xff, 0xfe]) });
            audioEnd += duration;
            gap = time - audioEnd;
          }
          // Ignore sub-frame timestamp rounding without accumulating the error.
          if (Math.abs(time - audioEnd) < 120) item.time = audioEnd;
          if (samples.length && item.time !== audioEnd) flush();
          samples.push(item);
          audioEnd = item.time + item.duration;
          if (samples.length >= 5) flush();
        }
      }
      // Keep WebRTC's decoder feedback and keyframe requests running.
      controller.enqueue(frame);
    },
  })).pipeTo(writable).catch(() => postMessage({ error: "Cannot prepare smooth playback" }));
}

if (typeof WorkerGlobalScope !== "undefined" && self instanceof WorkerGlobalScope) {
  self.onrtctransform = event => receive(event.transformer);
  self.onmessage = ({ data }) => {
    if (data.ack !== undefined) inFlight -= data.ack;
    else if (data.horizon !== undefined) { horizon = data.horizon; decodeVideo?.(); }
    else receive(data);
  };
}

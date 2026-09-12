<!-- SPDX-FileCopyrightText: 2026 wagaStrim contributors
     SPDX-License-Identifier: MIT -->

# Dynamic delay

Dynamic delay adds buffer when packets arrive late, then gradually returns to
the camera's normal delay. It is off by default and adjusts audio and video together.

Enable it in the camera settings to show:

- **Maximum delay:** up to 10000 ms, the default. Cannot be below the normal delay.
- **Catch-up speed:** 1 to 100 ms of delay removed per second. Defaults to 10 ms/s.
- **Jump fallback:** skip to a keyframe if the required delay exceeds the maximum.

Settings are saved per camera and apply live. Sync groups keep their fixed delay
and restore the saved dynamic settings when a camera leaves the group. The
[control API](../../docs/API.html) exposes the same options.

The relay samples the worst late arrival across both tracks once per second.
Buffering grows by up to 50 ms per second. After five quiet seconds, it shrinks
at up to the selected catch-up rate. Changing speed preserves the current delay
and any pending recovery; the new rate applies on the next tick.

A jump requests a keyframe, drops older queued media and shifts both tracks'
clocks together. The sender must provide the keyframe. Jumps skip content and can
pause playback. Without jumping, the target stops growing at the maximum;
packets can still arrive late. Existing RTP clock-drift protection still applies.

This folder holds the controller, dashboard controls and tests. The relay uses
its existing correction tick, with no extra timer or goroutine. Packets stay
encoded and keep their RTP timestamps. The player adds its own buffering, so
these settings cannot cap total playback latency. Faster catch-up may stutter.

Tests: `go test -race ./internal/dynamicdelay ./internal/relay ./internal/config ./internal/control`
and `node --test internal/dynamicdelay/controls.test.mjs`. Bonding tests cover
split traffic, duplicates and delayed paths. Local WagaWebRTC and Chrome H.264/Opus
runs covered catch-up, live settings and keyframe fallback. Moblin phone tests
had clean catch-up intervals at 20/50 ms/s; a viewer confirmed sync at 50. The
100 ms/s tests stuttered. Forced read-stall recovery remains unresolved. OBS,
simultaneous Wi-Fi/cellular playback, other codecs and a long soak remain unverified.

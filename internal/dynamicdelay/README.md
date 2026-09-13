<!-- SPDX-FileCopyrightText: 2026 wagaStrim contributors
     SPDX-License-Identifier: MIT -->

# Dynamic delay

Dynamic delay adds buffer when packets arrive late, then gradually returns to
the camera's normal delay. It is off by default and adjusts audio and video together.

Enable it in the camera settings to show:

- **Maximum delay:** up to 10000 ms, the default. Cannot be below the normal delay.
- **Automatic catch-up speed:** on by default. Scales from 1.01x to 1.2x as delay grows.
- **Catch-up speed:** shown when automatic speed is off. 1 to 200 ms of delay removed per second, with a saved default of 10 ms/s.
- **Jump fallback:** skip to a keyframe if the required delay exceeds the maximum.

Settings are saved per camera. Automatic/manual speed, maximum and jump changes apply live;
the built-in player reconnects when dynamic delay is switched on or off. Sync groups keep their fixed delay
and restore the saved dynamic settings when a camera leaves the group. The
[control API](../../docs/API.html) exposes the same options.

The relay samples the worst late arrival across both tracks once per second.
Buffering grows by up to 50 ms per second. After five quiet seconds, it shrinks
at up to the selected catch-up rate. Changing speed preserves the current delay
and any pending recovery; the new rate applies on the next tick.

Automatic speed starts at 10 ms/s and reaches 200 ms/s at 90% of the space
between normal and maximum delay. The player also counts its own excess buffer.
It changes acceleration in 0.01x steps, at most once per second, and keeps the
existing buffer thresholds to avoid stuttering or draining too far. Near the
normal delay it returns to 1x. Older camera settings default to automatic speed;
the API field `autoCatchUp: false` selects manual speed.

A jump requests a keyframe, drops older queued media and shifts both tracks'
clocks together. The sender must provide the keyframe. Jumps skip content and can
pause playback. Without jumping, the target stops growing at the maximum;
packets can still arrive late. Existing RTP clock-drift protection still applies.

This folder holds the controller, dashboard controls, player and tests. The relay uses
its existing correction tick, with no extra timer or goroutine. Packets stay
encoded and keep their RTP timestamps. The player adds its own buffering, so
these settings cannot cap total playback latency. The built-in player uses a shared
audio/video clock for H.264, H.265 or AV1 with Opus, with pitch-preserving catch-up up to
1.2x. It needs WebCodecs, MediaSource and encoded transforms on localhost or HTTPS.
Unsupported browsers and codecs use standard playback, which may stutter at higher speeds.
The player orders and deduplicates incoming video within its existing buffer.
A decoding error resumes at the next keyframe; three failures without a decoded
frame fall back to standard playback.
If relay recovery changes the audio/video clock offset, the player reconnects
to pick up fresh clock references instead of freezing the picture.
Once both sender reports arrive, the relay schedules audio and video on that
same timeline. Small clock corrections preserve queued media; a large timestamp
reset discards the affected queue and requests a keyframe before video resumes.

Tests: `go test -race ./internal/dynamicdelay ./internal/relay ./internal/config ./internal/control`
and `node --test internal/dynamicdelay/*.test.mjs`. Bonding tests cover split
traffic, duplicates and delayed paths. Local Chrome tests covered bitrate changes
and catch-up using WagaWebRTC for H.264/H.265 and Pion for AV1. WagaWebRTC does not
currently expose AV1. The custom player still needs
a phone/OBS sync check. Long outages, seven-viewer startup and simultaneous
Wi-Fi/cellular playback remain unverified.

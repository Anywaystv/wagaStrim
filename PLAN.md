# wagaStrim plan

A single Go binary that runs on a streamer's PC 24/7, accepts a WebRTC (WHIP) ingest from a
phone, and hands OBS a link to pull it back out as a Browser Source. Two links per camera, one
delay slider, one stats row. Nothing else.

Pion is the only media dependency. No RTSP, no SRT, no RTMP, no transcoding, no OBS plugin. The
reasoning for each rejection is in "Getting it into OBS" so it does not get relitigated.

The audience is IRL streamers who cannot set up an ingest. The whole product is: install, open
tray, copy sender link into Moblin, copy receiver link into OBS.

## Verified constraints

Everything below was checked against upstream sources, not assumed. These shape the design and
must not be silently designed around later.

| Claim | Status | Source |
| --- | --- | --- |
| Pion has a bonding feature | **No.** No bonding API, option, or package. `bonding` appears once org-wide, in the renomination blog post; `multipath` zero times. No bonding PRs, nothing in the `ice`/`webrtc`/`interceptor` commit logs. | grep over cloned `pion/ice` v4.4.1, `pion/webrtc` v4.2.18, `pion/interceptor` v0.1.47 |
| Pion can *receive* bonded media today | **Yes, unmodified.** See "Bonding" below. This is the load-bearing finding of the whole project. | `ice/candidate_base.go:309,346`; `srtp/context.go:34` |
| Automatic renomination bonds paths | **No.** It *switches* to the best candidate pair. One path carries media at a time. | pion.ly/blog/automatic-renomination |
| Moblin bonds over WHIP | **No.** Moblin bonds on SRTLA and RIST only. Its WHIP client opens one PeerConnection over the default route. | github.com/eerimoq/moblin README |
| iPhone can send AV1 | **No.** No AV1 hardware encoder on any iPhone. Moblin encodes H.264/AVC or H.265/HEVC. | Moblin README |
| Pion can carry H.265 and AV1 | **Yes.** H.265 payloader plus `h265reader`/`h265writer` landed in v4.1.0; AV1 stable since v4.1.0. | pion/webrtc v4.1.0, v4.2.0 releases |
| Pion FlexFEC helps our ingest | **Only if the sender emits it.** Moblin does not. `pion/interceptor/pkg/flexfec` ships `encoder_interceptor.go` but no matching decoder interceptor, and `flexfec_decoder_03.go` is a bare primitive. | pion/interceptor tree |
| OBS Media Source can pull WebRTC via FFmpeg options | **No.** Mainline FFmpeg has `libavformat/whip.c` (WHIP muxer, output) but no `webrtc_demux.c`, and the WHEP demuxer from the same patch series never merged. No Input Format or option string reaches code that is not compiled in. | HTTP 200 vs 404 on `FFmpeg/FFmpeg/master/libavformat/*` |
| Browser Source can decode HEVC | **Gated, not blocked.** Chromium defaults HEVC WebRTC receive on from M136 and has had it behind `WebRtcAllowH265Receive` since M126. OBS's CEF fork tops out at branch 6533 = Chromium 133: inside the flag window, below default-on. Untested whether OBS forwards the flag. | `obsproject/cef` branches; Chromium flag history |
| Pion has a current OBS project | **No.** One exists across all 63 pion repos, `pion/obs-wormhole`, archived Jan 2024. Its "WebRTC is in OBS now" note refers to WHIP *output* in OBS 30.0, not WHEP input. | pion org listing; obs-wormhole README |
| Pion exposes renomination | **Yes.** `SettingEngine.SetICERenomination`, with `WithRenominationGenerator`, `WithRenominationInterval`, `WithRenominationNominationAttribute`. | pion/webrtc v4.2.0 |

## Bonding

The renomination post ends by saying you can "send media over *all* candidates, because under
aggressive nomination they are all valid. This, actually, is connection bonding for free! There
are some practical considerations to getting this working well though, but that's another blog
post." That post was never written. But reading the source, the receive half is already true.

Three shipped behaviours combine into it:

1. **Every local candidate has its own read loop.** `candidate_base.go:309` spawns one per
   candidate, not one for the selected pair.
2. **Inbound media from any validated candidate is accepted.** `handleInboundPacket`
   (`candidate_base.go:346`) checks only that the source is a known remote candidate via
   `validateNonSTUNTraffic`. It never consults the selected pair. Every path writes into the same
   `agent.buf`.
3. **Duplicates are dropped for free.** `pion/srtp` wires a `replaydetector` per SSRC
   (`srtp/context.go:34`). A packet arriving on two paths is decrypted once and the second copy
   is discarded as a replay.

So a sender spraying the same SRTP stream across several interfaces lands as one merged,
deduplicated stream on our side, with no code from us. Renomination is what makes it hold: it
keeps every candidate port open and validated instead of closing the losers, which is the
precondition for paths 2..N still being routable.

### The two things that actually need work

**The replay window is the skew limit.** Bonded paths have wildly different latency: a good SIM at
30 ms alongside a congested one at 400 ms. The lagging path's packets arrive far behind the
sequence numbers the leading path already advanced. Once that gap exceeds the replay window they
are rejected as too old and the slow path silently contributes nothing. Size the window to the
worst tolerable skew via `SettingEngine.SetSRTPReplayProtectionWindow`. Never reach for
`disableSRTPReplayProtection`: it removes the deduplication the whole approach depends on.

**Per-path traffic counts are not obtainable, and an earlier draft promised them.** Two separate
limits, both checked in the source rather than assumed:

- `ice/candidate_base.go:373` attributes every received packet to the *selected* pair, in the only
  call site of `UpdatePacketReceived`. A packet arriving on the second SIM's socket is counted
  against the first SIM's pair. The merge happens before anything can attribute it.
- `pion/srtp` builds a `duplicatedError` carrying the SSRC and index when the replay detector
  rejects a packet, but that error is consumed inside the session read loop and never reaches the
  application. Deduplication is invisible from outside.

So a mis-sized replay window cannot be made visible the way this plan said it would be. What can
honestly be shown is how many candidate pairs are established and which one is nominated, which
tells a bonding user whether their extra paths exist at all. Attributing bytes to them would need
a change in pion, and claiming to do it with the data available would be a fabrication.

**The sender is the real blocker.** Moblin opens one PeerConnection over the default route and
sends over the selected pair only. Nothing on our side changes that. Options, in order of cost:

- Ship single-path now. Renomination already gives failover between Wi-Fi and cellular within
  seconds, which covers a large part of why people want bonding.
- Contribute multi-candidate sending upstream to Moblin.
- Write our own sender. Largest scope by far; a separate project, not a phase here.

Until a spraying sender exists, the UI and README say failover, not bonded. The receive path is
built and tested with a synthetic multi-path sender so it is ready when one arrives.

## Reachability

A phone on 5G cannot reach `127.0.0.1`. Cellular carriers run CGNAT and most home connections
sit behind NAT too, so ICE needs help. This is not optional polish, it is whether the product
works at all.

- Pin all media to **one** UDP port with `SettingEngine.SetICEUDPMux`, and serve signaling on one
  TCP port. Two ports to forward, which is a routable instruction for a non-technical streamer.
- Ship a reachability check in the UI that resolves the public address, probes the forwarded
  ports, and says plainly which one is closed.
- Offer a TURN relay field for users stuck behind CGNAT on both ends. Do not run one for them.

**Two listeners, not one.** The UI binds loopback and stays there. WHIP and WHEP signaling has to
be publicly reachable or the phone cannot connect at all. Keeping them on separate listeners is
what lets the UI be unauthenticated and the signaling be key-gated, and it stops a future change
from accidentally exposing the settings page to the internet.

## Deployments that own the ingest list

The desktop product is one person, one machine, one settings page. A second shape exists: a server
somewhere else provisions a machine, generates the keys, and treats this daemon as the media half
of something larger. `afk-stream` is the first of those, running one box per streamer that renders
a scene and encodes it, with the camera arriving here.

That shape needs three things the desktop one does not, and nothing else:

- **The keys arrive from outside.** Boxes are deleted and recreated on an idle timer, and a key
  minted here would hand the streamer a new push URL every time. `PUT /control/ingests` replaces
  the whole list with the document the owner holds, so a replaced machine converges by replaying
  it. Sessions whose keys or codecs moved are closed; a replay that changes nothing closes nothing.
- **Stats are read from another machine.** `GET /control/stats` is the map the settings page
  already renders, on a listener something other than a browser on this machine can reach.
- **A floor that suits the deployment.** See Delay above.

Both routes live on their own port, off unless `controlToken` is set, and every request is compared
against that token in constant time. It is deliberately not the signaling port, which has to be
open to the internet for WHIP: an admin surface there would be gated by the token alone, while a
separate port can also be scoped by a firewall to the one address allowed to call it. Failures
return the same 404 as an unknown route, so the port is not an oracle either. The settings page
stays on loopback and gains nothing.

A container image ships for this, static and headless, with the config directory as a volume. The
desktop install is still a binary and a tray icon.

## Stack

One Go binary. Pion for WebRTC, a tray icon, and a UI served on loopback from `embed.FS`. No
Node, no Electron, no second language. Same shape as `Anyways-BotGateway`, so the CSS tokens and
button language carry over directly.

React Native and Flutter were rejected: both drag a mobile UI runtime into a process whose job is
to sit in the background for weeks. Wails and Tauri were rejected as toolchain cost for a window
that shows two links and a slider.

## Layout

```
cmd/wagastrim/main.go      flags, wiring, signal handling
internal/config/           JSON config in os.UserConfigDir, atomic writes
internal/ingest/whip.go    WHIP: POST offer, DELETE teardown
internal/ingest/paths.go   per-candidate accounting for the bonded receive path
internal/relay/track.go    fan-out to subscribers
internal/relay/registry.go one entry per ingest, resolved by role-scoped key
internal/relay/syncgroup.go shared playout target across grouped ingests
internal/relay/delay.go    playout buffer + drift correction
internal/egress/whep.go    WHEP: POST offer, DELETE teardown
internal/egress/player/    WHEP player page for the OBS Browser Source path
internal/stats/            rolling bitrate, loss, RTT, jitter
internal/ui/               http handlers, embed.FS
internal/tray/             systray, open UI, quit (cgo; behind the notray tag)
internal/autostart/        per-platform login registration, one file each
scripts/impair.sh          netem / dnctl impairment for the test matrix
internal/ui/web/           embedded page, one HTML, one CSS, one JS, no framework
```

Target: under 3500 lines of production Go, tests excluded, revised up from 2000 once the buffer
and both signaling paths were written rather than estimated. If a package crosses 300 lines, that is a signal to re-read
it, not to split it reflexively.

## Behaviour

**Codec choice** restricts what the WHIP answer negotiates. H.264, H.265, and AV1 are registered
on the MediaEngine; the toggles decide which get offered. There is no transcoding anywhere in the
pipeline: the phone encodes, we forward bytes.

A codec has to clear both ends, and the two ends fail differently:

| Codec | Sender | Receiver (Browser Source) |
| --- | --- | --- |
| H.264 | Every phone | Always works |
| H.265 | Moblin's other option, and the one people pick on constrained cellular | Gated on the CEF flag, see "Getting it into OBS" |
| AV1 | **No iPhone can encode it.** Desktop and OBS-WHIP senders only | Works |

So AV1 is selectable but an iPhone will never pick it, and H.265 may negotiate fine and still
render black if the flag test fails. Both cases have to be stated next to the toggle at the moment
of choosing. A codec that cannot survive the whole path is not a codec option, it is a trap.

**Delay** is a playout buffer between ingest and egress. **Floor 2000 ms.** Default 2000, maximum
10000. The slider does not go below the floor and the config file is clamped on load, not trusted.

The floor is the whole point of the product, not a tuning preference. A phone on cellular loses
the link for a second or two constantly, whether a lift, an underpass, or a tower handoff. With two seconds
already buffered, OBS keeps being fed the entire time and the viewer sees nothing. Without it the
stream freezes on every dropout. A streamer who drags the slider to zero chasing latency has
turned off the reason they installed this.

The floor is what that case needs, not a constant of nature, and it is stated as a deployment
setting rather than a number in the code so it stays honest. `delayFloorMs` lowers it for a machine
whose camera reaches it over a LAN and whose player is the same box, where the dropout the floor
buys resilience against cannot happen and two seconds is latency spent on nothing. The hard bound is
100 ms, since below one frame interval a buffer has nothing to reorder, and the settings page never
shows the knob: it is written by whatever provisioned the machine. Drift correction follows the
target rather than a fixed two second margin, or a low target would never be corrected at all.

**The buffer only recovers packets if the NACK history is as deep as the buffer.** Pion's defaults
are not:

| Interceptor | Default | Covers at 8 Mbps (~833 pkt/s) |
| --- | --- | --- |
| `nack.GeneratorSize`, we NACK the phone | 512 | ~0.6 s |
| `nack.ResponderSize`, OBS NACKs us | 1024 | ~1.2 s |

Set both to 4096, which covers two seconds up to roughly 20 Mbps. Sizes are restricted to powers
of two; 4096 is a legal value for each. `ResponderSize` holds real packets, so budget about 5 MB
per stream; `GeneratorSize` is a bitmap and costs nothing. Leave `GeneratorInterval` at its 100 ms
default, which is twenty retry rounds inside the window.

**What the buffer actually buys, corrected.** An earlier draft said absorbing an outage works for
the full two seconds unconditionally "because it is just held bytes". That is wrong, and the
scheduling design is what makes it wrong.

Packets have to be released on a schedule derived from their RTP timestamp, not from when they
arrived. Arrival-based release just adds a fixed delay: a 1.5 second gap in arrivals becomes a 1.5
second gap in output, two seconds later. Timestamp-based release is what makes the buffer useful,
because a packet delayed or retransmitted still carries the timestamp saying where it belongs, and
as long as it lands before its slot the output has no hole at all.

So the two seconds buys time for late and retransmitted packets to still make their slot. It does
not conjure packets the sender never sent or already discarded. A true uplink outage where Moblin
drops frames from its own queue still leaves a gap, and no receiver-side buffer can fix that.
Recovery depth is additionally capped by how far back Moblin retains packets for retransmission,
which is its choice and not ours. Measure it against a real phone before claiming a number.

**Drift correction.** When buffered duration exceeds target plus 2000 ms, drain by dropping to the
next keyframe and firing a PLI, rather than playing faster. A pass-through relay cannot resample
video, and speeding up audio pitches it. One clean skip beats sustained distortion. The hysteresis
is deliberately wide so a link recovering from a dropout refills the buffer instead of triggering
a skip. Below target, hold and refill; never drain below the floor. Log every correction, and if a
stream corrects repeatedly, say so in the UI, because it means the target is too low for that
connection.

**Loss handling on ingest** is NACK and RTX plus the buffer above. Not FEC, because Moblin will not send
FlexFEC, so there is nothing to decode. Revisit only if we ship our own sender.

**Renomination** is on, so a phone moving between Wi-Fi and cellular re-homes without a
reconnect. Enabled via `SettingEngine.SetICERenomination`; note it acts on the controlling agent.

## Keys and links

**Two keys per ingest, never one.** Each ingest issues a separate sender key and receiver key,
both 32 hex from `crypto/rand`. This is not only about typos. With a single shared key, anyone
handed the OBS receiver link could also publish to the ingest, so a co-host given a feed to pull
could overwrite the broadcast. Separate keys make the receiver link safe to hand out and let
either side be revoked without disturbing the other, so a leaked OBS link is rotated mid-stream
without knocking the phone offline.

**Keys are self-identifying.** Prefix them by role, `s_` for sender and `r_` for receiver, so a
link says what it is even once unmasked and even after it has been pasted somewhere out of
context.

**Refuse a swapped link, and say which way round it goes.** Both WHIP and WHEP are a POST of
`application/sdp`, so the endpoint cannot tell the two clients apart by method or content type.
The key role is the mechanism:

| Presented | At | Response |
| --- | --- | --- |
| `r_...` | `/whip/` | 400, "This is the receiver link, which belongs in OBS. Moblin needs the sender link." |
| `s_...` | `/whep/` | 400, "This is the sender link, which belongs in Moblin. OBS needs the receiver link." |
| Unknown | either | 404, generic. No hint about which ingest exists. |

The helpful message is only ever returned to someone already holding a valid key for that ingest,
so it tells an attacker nothing. Compare with `crypto/subtle.ConstantTimeCompare` regardless.

**Check the SDP direction as a second signal.** A WHIP offer from Moblin carries sendonly media; a
WHEP offer from OBS carries recvonly. When the direction contradicts the endpoint, the diagnosis is
certain even if the keys somehow line up. Cheap, and it turns a mysterious black source into a
sentence that names the fix.

**Secrets in the UI.** Both links embed the public address and a key. Render them masked with an
eye toggle and a copy button, reusing the `.secret` block. Distinguish them by more than position:
each link is labelled with its destination application, not just its protocol, and the copy button
confirms which one was taken. Never log a full link, never put one in a stats payload, and allow
either key to be regenerated on its own.

## Multiple ingests

One ingest is the common case, but a chest cam plus a drone plus a handheld is the reason someone
outgrows a hosted service. Each ingest is an independent stream with its own pair of keys, its
own buffer, and its own stats row, appearing in OBS as its own source.

An ingest is a label, a sender key, a receiver key, a codec set, and a delay target. The label
matters more than it sounds. Eight unlabelled links that differ only by a hex string is how
someone ends up pointing their drone at their chest cam's scene.

**They all share the one UDP media port.** This is the load-bearing decision. Sessions are
distinguished by role-scoped key in the WHIP and WHEP paths, never by port, so adding a fourth camera
never means forwarding a fourth port. `SetICEUDPMux` already multiplexes this; do not let a future
change allocate a port per ingest, because that quietly destroys the two-ports promise the whole
setup story rests on.

**Sync groups.** Two cameras live on screen together will drift, because each has its own network
path and its own buffer. An ingest can join a named sync group, and every member of a group plays
out at the same target, the maximum of what its members need, so the worst path sets the pace for
all of them. Ingests are ungrouped by default: a chest cam and a drone that are only ever cut
between, never composited, should not be penalised by each other.

**Deleting an ingest revokes.** Tear down live sessions, invalidate both keys immediately, and
free the buffer. A deleted key must not work again, and either key must be regenerable on its own
without touching the other or the other ingests.

**Budget rather than a limit.** Each ingest costs roughly 10 MB of buffer plus its share of
bitrate and CPU. Do not hardcode a maximum. Measure actual CPU and total inbound bitrate, and warn
in the UI when adding another would exceed what this machine has been observed to handle. An
arbitrary cap of four is wrong on a Ryzen and wrong on a Pi.

Config is a versioned list from day one, not a single stream that grows an array later. There are
no users yet, so this costs nothing now and avoids a migration.

## Getting it into OBS

Settled after going round this three times; the reasoning is recorded so it does not get reopened.

**Pion and nothing else.** The receiver is a WHEP player page we serve, added to OBS as a Browser
Source. No `gortsplib`, no `gosrt`, no RTSP server, no MPEG-TS muxer, no RTMP. The page has to
exist anyway for the WHEP path, so the OBS receiver costs zero additional dependencies and zero
additional Go.

Every alternative was priced and rejected:

| Rejected | Why |
| --- | --- |
| FFmpeg options in Media Source | Impossible. Mainline FFmpeg has `libavformat/whip.c` but no `webrtc_demux.c`, and the WHEP demuxer from that patch series never merged. No option string reaches code that is not compiled in. |
| RTSP via `gortsplib` | Works, and costs no media conversion, but buys only HEVC, and buys it by routing around a version lag that fixes itself. One dependency to dodge a temporary gap is the kind of thing you regret. |
| SRT via `gosrt` | Full remux: depacketise, MPEG-TS mux, plus a muxer `pion/format/mpegts` does not have (it has `reader.go`, no writer). |
| RTMP / FLV | Worst of all. Forces an Opus→AAC transcode, which means cgo or an ffmpeg subprocess, which kills the static Linux build. H.264 only in practice. |
| Our own OBS plugin | C++, libobs, libdatachannel, three more platform targets. See below. |
| `m96-chan/OBS-WebRTC-Link` | Optional, never assumed. v0.1.5, eleven stars, last touched December 2025, GPL-2.0, no macOS build. Fine as a user's choice, wrong as our dependency. |

**HEVC is a version lag, not a limitation.** This is the one thing to get right, because the
earlier plan stated it wrongly. Chromium enables HEVC receive over WebRTC by default from **Chrome
136**, and has had it behind `WebRtcAllowH265Receive` / `WebRtcAllowH265Send` since **M126**. OBS's
CEF fork tops out at branch **6533**, which is Chromium **133**: three releases below default-on,
but inside the flag-gated window.

So HEVC into a Browser Source is not blocked, it is gated on whether OBS forwards CEF command-line
flags. Test `--enable-features=WebRtcAllowH265Receive` against a real OBS early; it is cheap and it
decides what the codec toggle may offer. Either way the gap closes on its own when OBS bumps CEF
past 136, which is why paying a dependency to route around it was the wrong trade.

**What Browser Source actually costs.** One CEF process per source. Irrelevant at one camera,
real at four. Measure it during the multi-ingest phase and put the number in the UI next to the
add-ingest warning rather than guessing.

**Setup, which the UI must show rather than bury in a README.** Sources → `+` → Browser, paste the
receiver link into URL, set Width and Height to the stream resolution, tick **Control audio via
OBS** so audio enters the mixer instead of the desktop, and untick **Shutdown source when not
visible** so scene cuts do not renegotiate the session every time. Those last two are the ones
people get wrong.

**Accept the key in the path or as a bearer token.** Our player page carries it in the path;
`Authorization: Bearer` is what the WHEP spec says and what any third-party client will send.
`/whep/r_...` and `/whep/` with the key in the header resolve identically. A few lines, and it keeps
the door open for clients we do not control without us depending on any of them.

### We are not writing an OBS plugin

Settled, so it does not get reopened. There is no Pion-based OBS plugin and there should not be
one. OBS plugins are C or C++ shared libraries against libobs, and both the core `obs-webrtc` plugin
that provides WHIP output and the third-party `OBS-WebRTC-Link` that provides WHEP input are C++
built on libdatachannel. Reaching Pion from there means `-buildmode=c-shared`, which puts an
entire Go runtime and garbage collector inside the OBS process with no clean unload when OBS
disables the plugin. The reason nobody has done this is a good one.

Pion's own attempt is instructive: `pion/obs-wormhole` is the only OBS project across all 63 pion
repos, and it is archived as of January 2024. Its README says WebRTC "has been added directly to
OBS", which refers to WHIP *output* in OBS 30.0 and says nothing about receiving into OBS.

## Performance

This process runs for weeks and forwards several megabits a second the whole time. The cost that
matters is per-packet work and allocation rate, not throughput headroom. At 8 Mbps that is roughly
830 packets a second inbound, each one touched by decrypt, interceptors, buffer, and one encrypt
per subscriber. Everything below was compile-checked against `pion/webrtc v4.2.18`.

**Never parse the payload.** This is a relay: bytes in, same bytes out. Read the RTP header, look
at nothing else. The one exception is keyframe detection for drift correction, which needs a few
bytes at the front of the video payload. Keep that in one small codec-switch function and let it
be the only place that knows what a NAL unit is.

**Size the kernel socket buffers, then check they took.** The default UDP receive buffer drops
packets under burst long before the application sees them, and this is the single biggest source
of unexplained loss on a self-hosted ingest.

```go
mux, err := ice.NewMultiUDPMuxFromPort(mediaPort,
	ice.UDPMuxFromPortWithReadBufferSize(8*1024*1024),
	ice.UDPMuxFromPortWithWriteBufferSize(8*1024*1024),
	ice.UDPMuxFromPortWithNetworks(ice.NetworkTypeUDP4, ice.NetworkTypeUDP6),
)
```

The trap: the kernel silently clamps to `net.core.rmem_max` on Linux and `kern.ipc.maxsockbuf` on
macOS, and `SetReadBuffer` returns nil anyway. Read the value back with `getsockopt(SO_RCVBUF)`,
compare it to what was asked for, and show the real number in the UI. A user seeing loss with a
208 KB buffer needs to be told to raise the sysctl, not left guessing.

**One mux, not a socket per connection.** `SetICEUDPMux` with a single port is both the
performance choice and the reason port forwarding is one line of instructions.

**Cut gathering work we cannot use.** Multicast DNS candidates are pointless for a public ingest
and add gathering latency; TCP candidates are not wanted for media.

```go
se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
se.SetReceiveMTU(1500)
```

**Prefer AES-GCM.** It beats AES-CM-HMAC-SHA1 on anything with AES-NI, which is every machine this
runs on. Offer it first and keep the old profile as fallback:
`se.SetSRTPProtectionProfiles(dtls.SRTP_AEAD_AES_128_GCM, dtls.SRTP_AES128_CM_HMAC_SHA1_80)`.

**Pool packet buffers.** A `sync.Pool` of fixed-size buffers, sized from the receive MTU, with the
buffer returned once the last subscriber has written it. Refcount rather than copy per subscriber.
Allocation rate, not heap size, is what drives GC frequency here.

**Marshal once for fan-out.** Encrypt is per-subscriber and unavoidable, but header rewriting and
marshalling are not. With one OBS receiver this is nearly free; keep it correct anyway so a second
receiver does not double the cost.

**Idle must be genuinely idle.** With no stream connected, expect near-zero CPU: no tickers
running, no poll loops, no stats recomputation. A tray app that burns 2% of a core forever is a
tray app people uninstall. Assert this in the soak test.

**Set `GOMEMLIMIT`** to a real ceiling. The steady-state working set is small and knowable, since a 2
second buffer plus a 4096-packet RTX buffer is roughly 10 MB per stream, so a soft limit turns a
leak into visible GC pressure instead of a machine slowly swapping at 4am.

Do not micro-optimise on instinct. Every change in this section needs a `go test -bench` or a
pprof profile in the PR showing it mattered. `net/http/pprof` is mounted on the loopback UI
listener, never on a public one.

## Testing

Unit tests do not tell you whether a stream survives a lift. Three layers, and the plan is not
done until all three run.

**Automated, in `go test`.** A synthetic WHIP sender built on Pion pushes a fixture file into the
server; a synthetic WHEP receiver pulls it and asserts every sequence number arrived. This is also
where the multi-path sender lives, the one that sprays the same SRTP across several local
candidates and proves the bonded receive path merges and dedupes. No phone required, runs in CI.

**Impairment tests on a real machine.** Wrap the automated harness in deliberate network damage
and assert behaviour, not just absence of crashes:

| Injected | Expected |
| --- | --- |
| 2% random loss | Recovered by NACK, no visible artifact |
| 400 ms jitter | Absorbed by the buffer, output cadence steady |
| Hard 2 s cut | Output never stalls, which is the floor doing its job |
| Hard 5 s cut | Stalls, then recovers to target without a permanent offset |
| One path of three killed | Others carry it, and the usable pair count drops |

Linux uses `tc qdisc netem`, macOS uses `dnctl` with `pfctl`. Script both in `scripts/impair.sh`
so the numbers are reproducible instead of anecdotal.

**Real hardware, before any release.** An actual iPhone running Moblin over actual cellular into
an actual OBS. Walk outside with it. Nothing else establishes that Moblin's WHIP client and our
answer agree, and no simulation substitutes for a real tower handoff. Record the measured
glass-to-glass latency and the observed NACK recovery depth; those two numbers go in the README
and nowhere else until they have been measured.

**Soak, 24 hours minimum,** since the entire premise is 24/7 residency. Watch RSS, goroutine
count, and open file descriptors. Flat lines or it does not ship. Idle CPU checked here too.
`GODEBUG=gctrace=1` for one run to confirm GC behaviour matches the `GOMEMLIMIT` expectation.

## Launch on start

A checkbox in settings, off by default, that registers the binary with whatever the platform
already uses. No custom daemon, no elevated privileges, nothing installed system-wide. All three
mechanisms are user-scoped and reversible by unticking the box.

| Platform | Mechanism |
| --- | --- |
| Windows | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` via `golang.org/x/sys/windows/registry`. User scope, no admin prompt. |
| Linux | A systemd user unit at `~/.config/systemd/user/wagastrim.service`. Headless boxes also need `loginctl enable-linger`, which the UI must say plainly rather than silently failing to start at boot. |
| macOS | A LaunchAgent plist in `~/Library/LaunchAgents`, loaded with `launchctl bootstrap gui/$UID`. |

Write the toggle so unticking removes the artifact completely, leaving no orphaned unit file and no dead
registry value pointing at a binary that moved. Verify the round trip in the soak checklist, and
verify it survives the binary being moved or upgraded.

## Platforms

Linux is a first-class target, not a port. A cheap mini PC or a Pi running headless next to the
router is arguably the *better* deployment for a 24/7 ingest than the streaming PC itself.

Build matrix: `linux/amd64`, `linux/arm64`, `windows/amd64`, `darwin/arm64`.

**The tray is the only platform-specific part, and macOS is where cgo enters, not Linux.** An
earlier draft of this plan said the Linux tray needs cgo. Measured, it does not: `fyne.io/systray`
talks StatusNotifierItem over `godbus/dbus`, which is pure Go, so `linux/amd64` builds with the
tray at `CGO_ENABLED=0`. macOS is the one that genuinely requires cgo.

Verified build matrix:

| Target | cgo | Tags | Result |
| --- | --- | --- | --- |
| linux/amd64 | 0 | `notray` | builds |
| linux/arm64 | 0 | `notray` | builds |
| linux/amd64 | 0 |  | builds, tray included |
| windows/amd64 | 0 |  | builds |
| darwin/arm64 | **1** |  | builds; fails at cgo=0 |

- Keep every package except `internal/tray` free of cgo and of build tags.
- Keep the `notray` tag and `--headless` anyway. GNOME still needs a shell extension for the icon
  to appear, and a headless box next to the router has no tray to put it in. Headless stays the
  default on Linux because a printed URL is a complete interface for a web UI, not because of cgo.
- Ship `linux/*` static. Only macOS needs a cgo toolchain.

This also makes the Linux story simpler than the Windows one: a systemd user service plus a URL,
with no icon to render. Document that path first in the README rather than treating it as the
degraded option.

Everything else is portable. Config lives in `os.UserConfigDir`, so no path branching. The socket
buffer sysctl differs per platform and is handled by reading the value back, as above.

## Phases

1. Skeleton: binary, config, tray, UI shell, health endpoint. No media.
2. WHIP ingest: one session, H.264 only, prove Moblin connects.
3. WHEP egress plus the Browser Source player page, prove OBS pulls it.
4. Delay buffer with the 2000 ms floor, deepened NACK history, and drift correction.
5. Stats and the reachability check.
6. H.265 and AV1 negotiation, codec toggles, per-codec receiver guidance in the UI.
7. Multiple ingests: registry, per-ingest links and stats, add and delete, sync groups.
8. Renomination and interface reporting.
9. Bonding receive path: replay window sizing, candidate pair reporting, and an honest account
   of what cannot be measured.
10. Autostart on all three platforms, including clean removal.
11. Performance pass: buffer pooling, socket sizing, pprof, benchmarks in the PR.
12. Soak and impairment runs, then real hardware with a phone outdoors.

Each phase is one PR and must be runnable at its end.

## Open

The one unresolved question that changes what ships:

- **Does OBS forward `--enable-features=WebRtcAllowH265Receive` to its CEF?** OBS's CEF is
  Chromium 133, inside the flag-gated window for HEVC over WebRTC but below the M136 default-on
  cutoff. If the flag reaches it, H.265 is a real codec option. If not, the toggle offers H.264
  and AV1 only until OBS bumps CEF. Cheap to test, gates phase 6, and it is the reason no media
  dependency was added. See "Getting it into OBS".

Settled, recorded so it is not rediscovered:

- Go 1.26.6 is installed at `~/.local/go`, symlinked into `~/.local/bin`, which `.zshrc` already
  has on PATH. `pion/webrtc v4.2.18` resolves and the renomination, replay-window, and NACK-sizing
  calls in this plan were compile-checked against it.
- Windows and Linux are both primary. Only the tray is platform-specific; see Platforms above.
- No GitHub repo until the project is done. Work stays in this directory. `MarcFryd/wagaStrim`
  gets created private at the end, then flipped public once the README is honest about what ships.
- License: MIT unless you say otherwise. Added at repo creation, not before.
- The README leads with the Browser Source setup, since that is the only step the target audience
  has to get right and two of its checkboxes are easy to miss.

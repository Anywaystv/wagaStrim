# Receive buffer and bandwidth control

WagaStrim uses a bounded packet queue after Pion authenticates and decrypts SRTP.
It preserves packet boundaries and arrival order. It does not wait for missing
packets or add a fixed jitter delay; camera playout delay remains separate.

- Each RTP SSRC allocates on demand (starting at 2 KB), grows up to 1 MB,
  and reuses that storage. Quiet tracks do not reserve the full limit.
- A full queue drops the new packet through Pion's `packetio.ErrFull` handling.
- Reads sleep until data, a deadline or closure. The queue starts no goroutine.
- Closing wakes all readers. Queued packets can drain before EOF.
- RTCP retains Pion's queue with its existing 100 KB limit.

Pion still provides SRTP authentication/replay protection, RTCP reports and NACK.
WHIP records TWCC timestamps at authenticated buffer input, before queueing;
slow track reads no longer change the reported arrival spacing. This is not a
kernel UDP timestamp: scheduling and decryption before buffer input remain included.
Recovered packets use their recovery arrival, not a guessed original arrival.
Only bound SSRCs with a negotiated TWCC extension are recorded. Pion's recorder
builds feedback every 100 ms, replacing its read-side TWCC loop for publishers.
Each publisher owns its collector, which closes with the peer. WHEP is unchanged.
Waga's encrypted shared-history/FEC recovery is unchanged.

Video negotiates RTX alongside its codec, allowing compatible senders to use
padding probes and RTX repair. BWE, probe scheduling and application-limited
region (ALR) detection belong to the sender: WagaWebRTC uses str0m for these.
WagaStrim returns feedback; it does not control the phone's encoder bitrate.

WHIP reserves one negotiation slot per camera. A replacement waits for the old
publisher's relay and subscriber cleanup; late callbacks cannot change the new
session's media or status. Multiple bonded paths still share one publisher session.

## Bonded delivery receipts

Recovery-enabled peers receive `WGR1` type 3 receipts: an eight-byte header
(`WGR1`, type, count, two zero bytes), followed by up to eight 16-byte SHA-256
prefixes of received encrypted RTP datagrams. Batches flush at eight packets or
on the next media arrival after 20 ms. No new goroutine or timer is started.
Ordinary WHIP peers do not enable this feedback; WHEP signaling is unchanged.

The sender matches each receipt to an outstanding packet and both socket
addresses. Duplicates and expired entries cannot release its delivery window.
These are ciphertext-arrival receipts, not proof of SRTP validation or decoding;
they do not replace SRTP authentication, TWCC, NACK, or FEC. An incomplete final
batch can expire at the sender. Receipt state is bounded to 256 paths per socket.

## Verify locally

WHIP sessions can move their return path when the selected ICE candidate has
been silent for two seconds. The replacement must send an authenticated binding
request and have completed a successful round trip. This keeps a stale Wi-Fi
selection from timing out a session carried by another bonded path. Healthy
selections stay put; total outages still use the normal ICE timeouts. The check
runs on incoming bindings, with no additional goroutine or polling timer.

```sh
go test -race ./internal/ingest ./internal/egress ./internal/relay
go test ./internal/ingest -run '^$' -bench BenchmarkReceiveBuffer -benchmem
```

Tests cover wraparound, overflow, packet boundaries, deadlines, repeated closure,
blocked-reader shutdown, codec negotiation and media forwarding.
TWCC tests cover unread queues, capture-time deltas, sequence wrap, duplicate
suppression, session isolation and shutdown. The warmed
single-reader benchmark measures allocations and throughput, not end-to-end
latency or CPU use with many cameras. No claim of outperforming Pion is made.

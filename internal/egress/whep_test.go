// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package egress_test

import (
	"sync"
	"testing"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/egress"
	"github.com/MarcFryd/wagaStrim/internal/ingest"
	"github.com/MarcFryd/wagaStrim/internal/relay"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeline builds the whole path: publisher, relay, subscriber.
func pipeline(t *testing.T) (*ingest.Server, *egress.Server, *config.Ingest) {
	t.Helper()

	cfg := &config.Config{Ingests: []config.Ingest{testIngest("Chest cam")}}

	engine, mux, err := ingest.NewSettingEngine(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mux.Close() })

	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	hub := relay.New()

	// Wired as main wires it.
	var whep *egress.Server

	whip, err := ingest.NewServer(cfg, log, engine, hub, stats.New(),
		func(id string) { whep.CloseIngest(id) })
	require.NoError(t, err)
	t.Cleanup(whip.Close)

	whep = egress.NewServer(cfg, log, whip.API(), hub)
	t.Cleanup(whep.Close)

	return whip, whep, &cfg.Ingests[0]
}

func gather(t *testing.T, peer *webrtc.PeerConnection) string {
	t.Helper()

	offer, err := peer.CreateOffer(nil)
	require.NoError(t, err)

	done := webrtc.GatheringCompletePromise(peer)
	require.NoError(t, peer.SetLocalDescription(offer))
	<-done

	return peer.LocalDescription().SDP
}

// publishWithFeedback attaches a synthetic Moblin and returns a frame writer
// plus the RTCP coming back up its link, which is where a keyframe request and
// a report of how the media is arriving both land.
func publishWithFeedback(
	t *testing.T,
	whip *ingest.Server,
	ing *config.Ingest,
) (func(), <-chan struct{}, <-chan *rtcp.ReceiverReport) {
	t.Helper()

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "synthetic")
	require.NoError(t, err)

	sender, err := peer.AddTrack(track)
	require.NoError(t, err)

	asked := make(chan struct{}, 16)
	reports := make(chan *rtcp.ReceiverReport, 16)

	go readFeedback(sender, asked, reports)

	answer, _, err := whip.Publish(ing.SenderKey, gather(t, peer))
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	frame := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x00, 0x00, 0x00, 0x01, 0x65, 0x88}

	return func() {
		_ = track.WriteSample(pionmedia.Sample{Data: frame, Duration: 33 * time.Millisecond})
	}, asked, reports
}

// readFeedback splits the RTCP coming back up a publisher's link into the two
// things a sender acts on: a request for a keyframe, and a report of how its
// media is arriving.
func readFeedback(sender *webrtc.RTPSender, asked chan<- struct{}, reports chan<- *rtcp.ReceiverReport) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}

		for _, packet := range packets {
			switch feedback := packet.(type) {
			case *rtcp.PictureLossIndication:
				select {
				case asked <- struct{}{}:
				default:
				}
			case *rtcp.ReceiverReport:
				select {
				case reports <- feedback:
				default:
				}
			}
		}
	}
}

// waitLive blocks until a subscriber can attach, which is the condition every
// test here depends on. Media only reaches the relay after ICE completes and
// the first packet lands.
func waitLive(t *testing.T, whep *egress.Server, ing *config.Ingest, writeFrame func()) {
	t.Helper()

	require.Eventually(t, func() bool {
		writeFrame()

		_, resource, err := whep.Subscribe(ing.ReceiverKey, recvOffer(t))
		if err != nil {
			return false
		}

		_ = whep.Teardown(resource)

		return true
	}, 20*time.Second, 200*time.Millisecond, "publisher never reached the relay")
}

func TestMediaReachesASubscriber(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame, _, _ := publishWithFeedback(t, whip, ing)
	waitLive(t, whep, ing, writeFrame)

	_, resource, err := whep.Subscribe(ing.ReceiverKey, recvOffer(t))
	require.NoError(t, err)
	assert.NotEmpty(t, resource)
	require.NoError(t, whep.Teardown(resource))
}

// playoutDelayURI is the extension a receiver reads to decide how much media to
// hold. A browser offers it; a stock pion peer does not, so the viewer below
// registers it to negotiate the way the real receiver does.
const playoutDelayURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"

// attachViewer subscribes a real receiver and returns it with the track it was
// given and the packets arriving on it, once media is actually flowing.
func attachViewer(
	t *testing.T,
	whep *egress.Server,
	ing *config.Ingest,
	writeFrame func(),
) (*webrtc.PeerConnection, *webrtc.TrackRemote, <-chan *rtp.Packet) {
	t.Helper()

	media := &webrtc.MediaEngine{}
	require.NoError(t, media.RegisterDefaultCodecs())
	require.NoError(t, media.RegisterHeaderExtension(
		webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI}, webrtc.RTPCodecTypeVideo))

	viewer, err := webrtc.NewAPI(webrtc.WithMediaEngine(media)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })

	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	carrying := make(chan *webrtc.TrackRemote, 1)
	packets := make(chan *rtp.Packet, 1)

	viewer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				return
			}

			select {
			case carrying <- track:
			default:
			}

			select {
			case packets <- pkt:
			default:
			}
		}
	})

	waitLive(t, whep, ing, writeFrame)

	answer, _, err := whep.Subscribe(ing.ReceiverKey, gather(t, viewer))
	require.NoError(t, err)
	require.NoError(t, viewer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	keepWriting(t, writeFrame)

	select {
	case track := <-carrying:
		return viewer, track, packets
	case <-time.After(20 * time.Second):
		require.Fail(t, "no RTP reached the subscriber")
	}

	return nil, nil, nil
}

func TestSubscriberReceivesPackets(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame, _, _ := publishWithFeedback(t, whip, ing)

	_, track, _ := attachViewer(t, whep, ing, writeFrame)

	assert.Equal(t, webrtc.MimeTypeH264, track.Codec().MimeType)
}

func TestSubscriberPLICrossesTheRelayToThePublisher(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame, asked, _ := publishWithFeedback(t, whip, ing)

	viewer, track, _ := attachViewer(t, whep, ing, writeFrame)

	// Attaching asks for a keyframe of its own. Waiting for the stream to go
	// quiet is what makes the request below the only explanation for the next
	// one, and it outlasts the throttle so ours is not the one dropped.
	drain(asked)

	require.Never(t, func() bool { return len(asked) > 0 }, 2*time.Second, 100*time.Millisecond,
		"a healthy stream must not be asking for keyframes on its own")

	require.NoError(t, viewer.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())},
	}))

	select {
	case <-asked:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the request never reached the publisher")
	}
}

// A receiver left to its own devices keeps the smallest buffer that keeps up,
// and plays at the edge of the arrival jitter. What OBS then re-encodes for
// Twitch is that stutter. The hold is asked for in the packets because the
// player page's own request needs a JavaScript API not every Browser Source has.
func TestPacketsAskTheReceiverToHold(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame, _, _ := publishWithFeedback(t, whip, ing)

	viewer, _, packets := attachViewer(t, whep, ing, writeFrame)

	var negotiated uint8

	for _, receiver := range viewer.GetReceivers() {
		for _, extension := range receiver.GetParameters().HeaderExtensions {
			// One-byte extension ids run 1 to 14, and the conversion below is
			// only safe inside that range.
			if extension.URI == playoutDelayURI && extension.ID > 0 && extension.ID < 15 {
				negotiated = uint8(extension.ID)
			}
		}
	}

	require.NotZero(t, negotiated, "the answer never negotiated the extension")

	require.Eventually(t, func() bool {
		select {
		case pkt := <-packets:
			return len(pkt.GetExtension(negotiated)) > 0
		case <-time.After(time.Second):
			return false
		}
	}, 20*time.Second, 100*time.Millisecond, "no packet carried a playout delay")

	pkt := <-packets

	var hold rtp.PlayoutDelayExtension
	require.NoError(t, hold.Unmarshal(pkt.GetExtension(negotiated)))

	assert.Equal(t, uint16(30), hold.MinDelay, "300ms in the extension's units of 10ms")
}

// A publisher's Sender Reports only reach the report interceptor if the ingest
// reads its RTCP, and unread, the phone has no round trip to adapt against.
func TestReceiverReportsCarryTheRoundTrip(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame, _, reports := publishWithFeedback(t, whip, ing)

	waitLive(t, whep, ing, writeFrame)

	keepWriting(t, writeFrame)

	deadline := time.After(30 * time.Second)

	for {
		select {
		case report := <-reports:
			for _, block := range report.Reports {
				if block.LastSenderReport != 0 {
					assert.NotZero(t, block.Delay, "a report naming an SR must say how long ago it arrived")

					return
				}
			}
		case <-deadline:
			require.Fail(t, "no receiver report named the publisher's last sender report")
		}
	}
}

// keepWriting drives the synthetic camera for the rest of a test, so the
// stream under examination is a running one rather than a single frame.
func keepWriting(t *testing.T, writeFrame func()) {
	t.Helper()

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	go func() {
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()

		for {
			select {
			case <-tick.C:
				writeFrame()
			case <-stop:
				return
			}
		}
	}()
}

// drain empties a feedback channel of whatever a test is not interested in.
func drain(feedback <-chan struct{}) {
	for {
		select {
		case <-feedback:
		default:
			return
		}
	}
}

func recvOffer(t *testing.T) string {
	t.Helper()

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	_, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	return gather(t, peer)
}

// Without this a subscriber sits connected and frozen for good once its
// publisher reconnects.
func TestPublisherEndingDisconnectsItsSubscribers(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame, _, _ := publishWithFeedback(t, whip, ing)

	viewer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })

	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	gone := make(chan struct{})
	closeOnce := sync.OnceFunc(func() { close(gone) })

	viewer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed:
			closeOnce()
		default:
		}
	})

	waitLive(t, whep, ing, writeFrame)

	answer, _, err := whep.Subscribe(ing.ReceiverKey, gather(t, viewer))
	require.NoError(t, err)
	require.NoError(t, viewer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	require.Eventually(t, func() bool {
		return viewer.ConnectionState() == webrtc.PeerConnectionStateConnected
	}, 20*time.Second, 100*time.Millisecond, "the subscriber never connected")

	whip.CloseIngest(ing.ID)

	select {
	case <-gone:
	case <-time.After(30 * time.Second):
		require.Fail(t, "the subscriber was left on a track nothing will ever write to again")
	}
}

func TestSenderKeyAtWhepIsRefused(t *testing.T) {
	_, whep, ing := pipeline(t)

	_, _, err := whep.Subscribe(ing.SenderKey, recvOffer(t))
	assert.ErrorIs(t, err, egress.ErrWrongRole, "the Moblin link must not subscribe")
}

func TestSubscribingToAnIdleIngestSaysSo(t *testing.T) {
	_, whep, ing := pipeline(t)

	_, _, err := whep.Subscribe(ing.ReceiverKey, recvOffer(t))
	assert.ErrorIs(t, err, egress.ErrOffline, "an idle camera is not a negotiation failure")
}

func TestSendonlyOfferIsRefused(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame, _, _ := publishWithFeedback(t, whip, ing)
	waitLive(t, whep, ing, writeFrame)

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	_, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	require.NoError(t, err)

	_, _, err = whep.Subscribe(ing.ReceiverKey, gather(t, peer))
	assert.ErrorIs(t, err, egress.ErrNotReceiving)
}

func TestPlayerPageCarriesNoConfiguration(t *testing.T) {
	page, err := egress.PlayerPage()
	require.NoError(t, err)

	assert.Contains(t, string(page), "/player/", "the page derives its endpoint from its own path")
	assert.NotContains(t, string(page), "s_", "no sender key may appear in a page served to viewers")
}

// testIngest builds a camera with fixed keys. Real randomness buys a test
// nothing and a readable key makes a failure easier to place.
func testIngest(label string) config.Ingest {
	return config.Ingest{
		ID:          "cam-" + label,
		Label:       label,
		SenderKey:   config.SenderPrefix + "00000000000000000000000000000001",
		ReceiverKey: config.ReceiverPrefix + "00000000000000000000000000000002",
		Codecs:      []string{config.CodecH264},
		DelayMS:     config.DelayDefaultMS,
	}
}

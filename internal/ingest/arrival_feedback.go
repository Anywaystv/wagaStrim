// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/transport/v4/packetio"
	"github.com/pion/webrtc/v4"
)

// One collector belongs to one publisher API/PeerConnection. WHEP keeps its
// shared API and stock interceptor. No state is keyed across connections.
type arrivalFeedback struct {
	interceptor.NoOp
	mu                       sync.Mutex
	wg                       sync.WaitGroup
	done                     chan struct{}
	closed, claimed, started bool
	start                    time.Time
	streams                  map[uint32]uint8
	header                   rtp.Header
	recorder                 *twcc.Recorder
	log                      logging.LeveledLogger
}

func newArrivalFeedback(log logging.LeveledLogger) *arrivalFeedback {
	return &arrivalFeedback{
		done: make(chan struct{}), start: time.Now(), streams: make(map[uint32]uint8),
		recorder: twcc.NewRecorder(rand.Uint32()), log: log, // #nosec G404 -- RTCP SSRC is an identifier, not a secret.
	}
}

func configureArrivalFeedback(
	media *webrtc.MediaEngine, registry *interceptor.Registry, arrival *arrivalFeedback,
) error {
	if arrival == nil {
		return webrtc.ConfigureTWCCSender(media, registry)
	}
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		media.RegisterFeedback(webrtc.RTCPFeedback{Type: webrtc.TypeRTCPFBTransportCC}, kind)
		if err := media.RegisterHeaderExtension(
			webrtc.RTPHeaderExtensionCapability{URI: sdp.TransportCCURI}, kind,
		); err != nil {
			return err
		}
	}
	registry.Add(arrival)

	return nil
}

func (f *arrivalFeedback) NewInterceptor(string) (interceptor.Interceptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed || f.closed {
		return nil, fmt.Errorf("%w: arrival feedback collector cannot be reused across peers", ErrBuildAPI)
	}
	f.claimed = true

	return f, nil
}

func (f *arrivalFeedback) buffer(kind packetio.BufferPacketType, ssrc uint32) io.ReadWriteCloser {
	if kind != packetio.RTPBufferPacket {
		return receiveBuffer(kind, ssrc)
	}
	b := newPacketBuffer()
	b.onPacket = f.record

	return b
}

func (f *arrivalFeedback) BindRemoteStream(
	info *interceptor.StreamInfo, reader interceptor.RTPReader,
) interceptor.RTPReader {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		for _, ext := range info.RTPHeaderExtensions {
			if ext.URI == sdp.TransportCCURI && ext.ID > 0 && ext.ID <= 255 {
				f.streams[info.SSRC] = uint8(ext.ID)

				break
			}
		}
	}

	return reader
}

func (f *arrivalFeedback) UnbindRemoteStream(info *interceptor.StreamInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.streams, info.SSRC)
}

// Buffer writes have already passed SRTP authentication and replay rejection.
// Record even on queue overflow: TWCC describes network receipt, not playout.
func (f *arrivalFeedback) record(packet []byte, arrival time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	if _, err := f.header.Unmarshal(packet); err != nil {
		return
	}
	id := f.streams[f.header.SSRC]
	if id == 0 {
		return
	}
	var ext rtp.TransportCCExtension
	if ext.Unmarshal(f.header.GetExtension(id)) != nil {
		return
	}
	f.recorder.Record(f.header.SSRC, ext.TransportSequence, arrival.Sub(f.start).Microseconds())
}

func (f *arrivalFeedback) feedback() []rtcp.Packet {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}

	return f.recorder.BuildFeedbackPacket()
}

func (f *arrivalFeedback) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.started {
		return writer
	}
	f.started = true
	f.wg.Go(func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-ticker.C:
				if packets := f.feedback(); len(packets) != 0 {
					if _, err := writer.Write(packets, nil); err != nil {
						f.log.Errorf("arrival TWCC: %v", err)
					}
				}
			}
		}
	})

	return writer
}

func (f *arrivalFeedback) Close() error {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		clear(f.streams)
		close(f.done)
	}
	f.mu.Unlock()
	f.wg.Wait()

	return nil
}

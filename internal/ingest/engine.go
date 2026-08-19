// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"fmt"

	"github.com/pion/dtls/v3"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// Socket buffers large enough to absorb a burst. The kernel silently clamps to
// net.core.rmem_max on Linux and kern.ipc.maxsockbuf on macOS, and the setter
// reports success either way, so the effective size is read back and surfaced
// rather than assumed.
const socketBuffer = 8 << 20

// replayWindow has to exceed the sequence skew between bonded paths. A packet
// arriving further behind than this is discarded as a replay, which silently
// removes the slow path's contribution. Never disable replay protection: it is
// also the deduplication that makes multi-path receive work.
const replayWindow = 4096

// NewSettingEngine builds the shared ICE and DTLS configuration.
func NewSettingEngine(mediaPort int) (*webrtc.SettingEngine, *ice.MultiUDPMuxDefault, error) {
	mux, err := ice.NewMultiUDPMuxFromPort(mediaPort,
		ice.UDPMuxFromPortWithReadBufferSize(socketBuffer),
		ice.UDPMuxFromPortWithWriteBufferSize(socketBuffer),
		ice.UDPMuxFromPortWithNetworks(ice.NetworkTypeUDP4, ice.NetworkTypeUDP6),
		// Off by default in pion. Without it the mux binds the LAN address only,
		// so a receiver on this same machine has no candidate it can reach.
		ice.UDPMuxFromPortWithLoopback(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: udp mux on %d: %w", ErrBuildAPI, mediaPort, err)
	}

	engine := &webrtc.SettingEngine{}

	// One forwarded UDP port for every ingest and every session. Allocating a
	// port per ingest would turn "forward two ports" into "forward one per
	// camera", which is the setup story the product depends on.
	engine.SetICEUDPMux(mux)

	// Keeps a phone connected across a Wi-Fi to cellular handoff instead of
	// forcing a reconnect, and keeps losing candidates open and validated.
	if err := engine.SetICERenomination(); err != nil {
		return nil, nil, fmt.Errorf("%w: renomination: %w", ErrBuildAPI, err)
	}

	engine.SetSRTPReplayProtectionWindow(replayWindow)

	// Multicast DNS candidates cannot help a public ingest and only slow
	// gathering; TCP candidates are not wanted for media.
	engine.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	engine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})

	// AES-GCM beats AES-CM-HMAC-SHA1 on anything with AES-NI. The older profile
	// stays as a fallback for clients that do not offer GCM.
	engine.SetSRTPProtectionProfiles(
		dtls.SRTP_AEAD_AES_128_GCM,
		dtls.SRTP_AES128_CM_HMAC_SHA1_80,
	)

	return engine, mux, nil
}

// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"fmt"
	"net"
	"strings"

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
func NewSettingEngine(mediaPort int, publicIPs ...string) (*webrtc.SettingEngine, *ice.MultiUDPMuxDefault, error) {
	externalIPs, err := publicICEAddresses(publicIPs)
	if err != nil {
		return nil, nil, err
	}

	muxOptions := []ice.UDPMuxFromPortOption{
		ice.UDPMuxFromPortWithReadBufferSize(socketBuffer),
		ice.UDPMuxFromPortWithWriteBufferSize(socketBuffer),
		ice.UDPMuxFromPortWithNetworks(ice.NetworkTypeUDP4, ice.NetworkTypeUDP6),
		ice.UDPMuxFromPortWithIPFilter(func(ip net.IP) bool {
			return !ip.IsLinkLocalUnicast()
		}),
		// Off by default in pion. Without it the mux binds the LAN address only,
		// so a receiver on this same machine has no candidate it can reach.
		ice.UDPMuxFromPortWithLoopback(),
	}
	mux, err := ice.NewMultiUDPMuxFromPort(mediaPort, muxOptions...)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: udp mux on %d: %w", ErrBuildAPI, mediaPort, err)
	}

	engine := &webrtc.SettingEngine{}
	if len(externalIPs) > 0 {
		// Append the public address instead of replacing the local candidates.
		// A compositor beside wagaStrim uses its Docker or LAN candidate, while
		// remote publishers and viewers use the forwarded public candidate.
		// The shared mux receives on wildcard sockets, so candidate rewriting
		// observes 0.0.0.0/:: rather than an individual interface address. Use a
		// family-wide append rule while continuing to accept legacy external/local
		// configuration values above.
		if err := engine.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        externalIPs,
			AsCandidateType: webrtc.ICECandidateTypeSrflx,
			Mode:            webrtc.ICEAddressRewriteAppend,
		}); err != nil {
			return nil, nil, fmt.Errorf("%w: public ICE address: %w", ErrBuildAPI, err)
		}
	}

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

func publicICEAddresses(mappings []string) ([]string, error) {
	externalIPs := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		parts := strings.Split(mapping, "/")
		valid := len(parts) >= 1 && len(parts) <= 2
		for _, part := range parts {
			valid = valid && net.ParseIP(strings.TrimSpace(part)) != nil
		}
		if !valid {
			return nil, fmt.Errorf("%w: invalid public ICE IP mapping %q", ErrBuildAPI, mapping)
		}
		externalIPs = append(externalIPs, strings.TrimSpace(parts[0]))
	}

	return externalIPs, nil
}

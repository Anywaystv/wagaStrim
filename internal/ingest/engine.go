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
	recoveryNetwork, err := newRecoveryNet()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: recovery network: %w", ErrBuildAPI, err)
	}
	muxOptions = append(muxOptions, ice.UDPMuxFromPortWithNet(recoveryNetwork))
	mux, err := ice.NewMultiUDPMuxFromPort(mediaPort, muxOptions...)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: udp mux on %d: %w", ErrBuildAPI, mediaPort, err)
	}

	engine := &webrtc.SettingEngine{}
	engine.BufferFactory = receiveBuffer
	if len(externalIPs) > 0 {
		// Append the public address instead of replacing the local candidates.
		// A compositor beside wagaStrim uses its Docker or LAN candidate, while
		// remote publishers and viewers use the forwarded public candidate.
		// Rewrite mux host candidates so the forwarded port stays the media port.
		// A synthetic srflx candidate allocates a separate, unforwarded socket.
		var rules []webrtc.ICEAddressRewriteRule
		for _, mapping := range externalIPs {
			external, local, _ := strings.Cut(mapping, "/")
			rules = append(rules, webrtc.ICEAddressRewriteRule{
				External: []string{external}, Local: local,
				AsCandidateType: webrtc.ICECandidateTypeHost,
				Mode:            webrtc.ICEAddressRewriteAppend,
			})
		}
		if err := engine.SetICEAddressRewriteRules(rules...); err != nil {
			return nil, nil, fmt.Errorf("%w: public ICE address: %w", ErrBuildAPI, err)
		}
	}

	// One forwarded UDP port for every ingest and every session. Allocating a
	// port per ingest would turn "forward two ports" into "forward one per
	// camera", which is the setup story the product depends on.
	engine.SetICEUDPMux(newRecoveryMux(mux, recoveryNetwork.state))

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
		for i, part := range parts {
			parts[i] = strings.TrimSpace(part)
			valid = valid && net.ParseIP(strings.TrimSpace(part)) != nil
		}
		if !valid {
			return nil, fmt.Errorf("%w: invalid public ICE IP mapping %q", ErrBuildAPI, mapping)
		}
		// Keep the local half so the public candidate uses the forwarded socket,
		// not whichever interface the mux happens to enumerate first.
		externalIPs = append(externalIPs, strings.Join(parts, "/"))
	}

	return externalIPs, nil
}

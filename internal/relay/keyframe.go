// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import "strings"

// This is the only place in the relay that looks inside a payload. Everywhere
// else packets are opaque bytes. Drift correction needs to know where it can
// resume without showing corrupt video, and that means finding a keyframe.
const (
	h264TypeMask   = 0x1F
	h264IDR        = 5
	h264SPS        = 7
	h264STAPA      = 24
	h264FUA        = 28
	h264FUStartBit = 0x80
)

// isKeyframe reports whether a payload begins a decodable picture.
func isKeyframe(mime string, payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	if strings.EqualFold(mime, "video/H264") {
		return h264Keyframe(payload)
	}

	// An unknown codec must not be treated as never-keyframing, or drift
	// correction would drop forever. Phase 6 adds H.265 and AV1; until then the
	// safe answer is to resume immediately rather than stall.
	return true
}

func h264Keyframe(payload []byte) bool {
	switch nalType := payload[0] & h264TypeMask; nalType {
	case h264IDR, h264SPS:
		return true
	case h264STAPA:
		return stapaHasKeyframe(payload)
	case h264FUA:
		return fuaStartsKeyframe(payload)
	default:
		return false
	}
}

// stapaHasKeyframe walks an aggregation packet looking for an IDR or parameter set.
func stapaHasKeyframe(payload []byte) bool {
	const headerLen = 1

	for offset := headerLen; offset+2 <= len(payload); {
		size := int(payload[offset])<<8 | int(payload[offset+1])
		offset += 2

		if size == 0 || offset+size > len(payload) {
			return false
		}

		switch payload[offset] & h264TypeMask {
		case h264IDR, h264SPS:
			return true
		}

		offset += size
	}

	return false
}

// fuaStartsKeyframe reports whether this is the first fragment of an IDR.
func fuaStartsKeyframe(payload []byte) bool {
	const minLen = 2

	if len(payload) < minLen {
		return false
	}

	if payload[1]&h264FUStartBit == 0 {
		return false
	}

	return payload[1]&h264TypeMask == h264IDR
}

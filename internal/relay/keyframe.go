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

// H.265 payload header: type is bits 1..6 of the first byte (RFC 7798).
// IRAP pictures are 16 through 23; the parameter sets start one after.
const (
	h265TypeShift = 1
	h265TypeMask  = 0x3F
	h265IRAPFirst = 16
	h265IRAPLast  = 23
	h265VPS       = 32
	h265PPS       = 34
	h265AP        = 48
	h265FU        = 49
	h265FUStart   = 0x80
	h265FUType    = 0x3F
)

// AV1 aggregation header: the N bit marks the first packet of a coded video
// sequence, which is the only keyframe signal available without parsing OBUs.
const av1NMask = 0b0000_1000

// isKeyframe reports whether a payload begins a decodable picture.
func isKeyframe(mime string, payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	switch {
	case strings.EqualFold(mime, "video/H264"):
		return h264Keyframe(payload)
	case strings.EqualFold(mime, "video/H265"):
		return h265Keyframe(payload)
	case strings.EqualFold(mime, "video/AV1"):
		return payload[0]&av1NMask != 0
	default:
		// A codec nobody taught this function must not be treated as never
		// keyframing, or drift correction would drop until the stream ended.
		// Resuming on the wrong packet shows one corrupt frame; never resuming
		// shows nothing at all.
		return true
	}
}

func h265Keyframe(payload []byte) bool {
	switch nalType := (payload[0] >> h265TypeShift) & h265TypeMask; {
	case nalType >= h265IRAPFirst && nalType <= h265IRAPLast:
		return true
	case nalType >= h265VPS && nalType <= h265PPS:
		return true
	case nalType == h265AP:
		return h265AggregationHasKeyframe(payload)
	case nalType == h265FU:
		return h265FragmentStartsKeyframe(payload)
	default:
		return false
	}
}

// h265AggregationHasKeyframe walks an aggregation packet. Unlike H.264 the
// payload header is two bytes, and each unit is length-prefixed.
func h265AggregationHasKeyframe(payload []byte) bool {
	const headerLen = 2

	for offset := headerLen; offset+2 <= len(payload); {
		size := int(payload[offset])<<8 | int(payload[offset+1])
		offset += 2

		if size < headerLen || offset+size > len(payload) {
			return false
		}

		nalType := (payload[offset] >> h265TypeShift) & h265TypeMask
		if (nalType >= h265IRAPFirst && nalType <= h265IRAPLast) ||
			(nalType >= h265VPS && nalType <= h265PPS) {
			return true
		}

		offset += size
	}

	return false
}

// h265FragmentStartsKeyframe reports whether this is the first fragment of an
// IRAP picture. The fragmentation header is the third byte, after the two byte
// payload header.
func h265FragmentStartsKeyframe(payload []byte) bool {
	const minLen = 3

	if len(payload) < minLen || payload[2]&h265FUStart == 0 {
		return false
	}

	nalType := payload[2] & h265FUType

	return nalType >= h265IRAPFirst && nalType <= h265IRAPLast
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

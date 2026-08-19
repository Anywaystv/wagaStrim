// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package bonded

import (
	"net"
	"sync"

	"github.com/pion/transport/v4"
)

// routing decides which socket a media packet leaves by.
type routing int

const (
	// splitPaths sends each packet down one path only, alternating. What
	// arrives proves the ingest accepts media from a candidate that is not the
	// selected one, which is the merge half of bonding.
	splitPaths routing = iota

	// sprayPaths sends every packet down every path, which is what a bonded
	// sender does. What arrives proves the SRTP replay detector discards the
	// copies rather than the relay forwarding each packet several times.
	sprayPaths
)

// mediaFloor and mediaCeiling bound the first byte of an RTP or SRTP packet.
// STUN sits below and DTLS between, and neither may be duplicated: connectivity
// checks and the handshake belong to the socket that started them.
const (
	mediaFloor   = 128
	mediaCeiling = 192
)

// paths holds every socket the sender gathered a candidate on. ICE opens one
// per local address, so registering them as they are created is what turns a
// single PeerConnection into a multi-path sender without touching pion.
type paths struct {
	mu     sync.Mutex
	conns  []transport.UDPConn
	mode   routing
	copies int
}

func (p *paths) add(conn transport.UDPConn) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.conns = append(p.conns, conn)
}

func (p *paths) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.conns)
}

// wrote records how many sockets accepted one packet. Without this a spray that
// silently failed on every socket but one would look like deduplication.
func (p *paths) wrote(sockets int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sockets > 1 {
		p.copies += sockets - 1
	}
}

// duplicates is how many copies were put on the wire beyond the first.
func (p *paths) duplicates() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.copies
}

// carriers returns the sockets a packet with this sequence number leaves by.
func (p *paths) carriers(seq uint16) []transport.UDPConn {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.mode == sprayPaths || len(p.conns) == 0 {
		return p.conns
	}

	pick := int(seq) % len(p.conns)

	return p.conns[pick : pick+1]
}

// sprayNet wraps the real network so every socket ICE opens is registered.
type sprayNet struct {
	transport.Net

	paths *paths
}

func (n *sprayNet) ListenUDP(network string, locAddr *net.UDPAddr) (transport.UDPConn, error) {
	conn, err := n.Net.ListenUDP(network, locAddr)
	if err != nil {
		return nil, err
	}

	// The registry holds the raw socket, not the wrapper. Fanning out through
	// the wrapper would route every copy again, which is a recursion.
	n.paths.add(conn)

	return &sprayConn{UDPConn: conn, paths: n.paths}, nil
}

// sprayConn is one path. Media written to it is re-routed across the whole set,
// while everything else stays on the socket that produced it.
type sprayConn struct {
	transport.UDPConn

	paths *paths
}

func (c *sprayConn) WriteTo(payload []byte, addr net.Addr) (int, error) {
	if len(payload) < 4 || payload[0] < mediaFloor || payload[0] >= mediaCeiling {
		return c.UDPConn.WriteTo(payload, addr)
	}

	// SRTP encrypts the payload and leaves the header in the clear, so the
	// sequence number is readable here and can pick the path.
	seq := uint16(payload[2])<<8 | uint16(payload[3])

	sockets := 0

	for _, conn := range c.paths.carriers(seq) {
		// A write out of the socket that does not hold the route to this
		// destination is the case under test, not a failure to report: the
		// receiver either accepts it as a second path or it never arrives.
		if _, err := conn.WriteTo(payload, addr); err == nil {
			sockets++
		}
	}

	c.paths.wrote(sockets)

	return len(payload), nil
}

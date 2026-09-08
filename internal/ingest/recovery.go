// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4"
	"github.com/pion/transport/v4/stdnet"
)

const (
	recoveryParityType  = 1
	recoveryRequestType = 2
	recoveryHeaderSize  = 12
	recoveryHistorySize = 4096
	recoveryGroupLimit  = 256
	recoveryStreamLimit = 256
	recoveryRequestSize = 32
	recoveryRetry       = 50 * time.Millisecond
)

type recoveryNet struct {
	transport.Net
	log   logging.LeveledLogger
	state *recoveryState
}

func newRecoveryNet() (*recoveryNet, error) {
	network, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}

	return &recoveryNet{
		Net:   network,
		log:   logging.NewDefaultLoggerFactory().NewLogger("recovery"),
		state: newRecoveryState(),
	}, nil
}

func (n *recoveryNet) ListenUDP(network string, address *net.UDPAddr) (transport.UDPConn, error) {
	conn, err := n.Net.ListenUDP(network, address)
	if err != nil {
		return nil, err
	}

	return &recoveryConn{UDPConn: conn, log: n.log, recoveryState: n.state}, nil
}

type recoveryPacketID struct {
	remote         string
	ssrc           uint32
	sequenceNumber uint16
}

type recoveryStreamID struct {
	remote string
	ssrc   uint32
}

type recoveredDatagram struct {
	data []byte
	addr net.Addr
}

type recoveryStream struct {
	highest     uint16
	based       bool
	enabled     bool
	missing     map[uint16]time.Time
	lastTouched time.Time
}

type recoveryEntry struct {
	id     recoveryPacketID
	length int
}

type recoveryGroup struct {
	entries []recoveryEntry
	parity  []byte
	remote  net.Addr
}

type recoveryGroupID struct {
	remote string
	ssrc   uint32
	last   uint16
}

type recoveryConn struct {
	transport.UDPConn
	log logging.LeveledLogger
	*recoveryState
	ready           []recoveredDatagram
	pendingBindings map[recoveryBindingID]string
	identities      map[string]string
}

type recoveryBindingID struct {
	remote      string
	transaction [stun.TransactionIDSize]byte
}

type recoveryState struct {
	mu           sync.Mutex
	packets      map[recoveryPacketID][]byte
	packetOrder  []recoveryPacketID
	packetHead   int
	streams      map[recoveryStreamID]*recoveryStream
	groups       map[recoveryGroupID]recoveryGroup
	groupOrder   []recoveryGroupID
	groupHead    int
	packetsSince uint64
}

func newRecoveryConn(conn transport.UDPConn, log logging.LeveledLogger) *recoveryConn {
	return &recoveryConn{
		UDPConn:       conn,
		log:           log,
		recoveryState: newRecoveryState(),
	}
}

func newRecoveryState() *recoveryState {
	return &recoveryState{
		packets: make(map[recoveryPacketID][]byte),
		streams: make(map[recoveryStreamID]*recoveryStream),
		groups:  make(map[recoveryGroupID]recoveryGroup),
	}
}

func (c *recoveryConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	for {
		if datagram, ok := c.popReady(); ok {
			return copy(buffer, datagram.data), datagram.addr, nil
		}

		read, remote, err := c.UDPConn.ReadFrom(buffer)
		if err != nil {
			return read, remote, err
		}

		if hasRecoveryMagic(buffer[:read]) {
			if datagram, ok := c.consumeParity(buffer[:read], remote); ok {
				return copy(buffer, datagram.data), datagram.addr, nil
			}

			continue
		}

		id, ok := recoveryRTPPacketID(buffer[:read])
		if !ok {
			c.observeBinding(buffer[:read], remote, false)

			return read, remote, nil
		}

		id.remote = c.identity(remote)
		request := c.remember(id, buffer[:read], remote)
		if len(request) > 0 {
			c.sendRequest(request, remote)
		}

		return read, remote, nil
	}
}

func (c *recoveryConn) popReady() (recoveredDatagram, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.ready) == 0 {
		return recoveredDatagram{}, false
	}

	datagram := c.ready[0]
	copy(c.ready, c.ready[1:])
	c.ready = c.ready[:len(c.ready)-1]

	return datagram, true
}

func (c *recoveryConn) remember(id recoveryPacketID, data []byte, remote net.Addr) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.storePacketLocked(id, data)
	now := time.Now()
	stream := c.streamLocked(recoveryStreamID{remote: id.remote, ssrc: id.ssrc}, now)
	stream.lastTouched = now
	delete(stream.missing, id.sequenceNumber)
	c.trackGapLocked(id, stream)
	c.recoverPendingLocked(id, remote)
	c.packetsSince++
	if c.packetsSince%recoveryHistorySize == 0 {
		c.trimStreamsLocked(now)
	}

	if !stream.enabled {
		return nil
	}

	return makeRecoveryRequest(id.ssrc, dueRepairs(stream, now))
}

func (c *recoveryConn) consumeParity(data []byte, remote net.Addr) (recoveredDatagram, bool) {
	group, id, ok := parseRecoveryGroup(data, remote)
	if !ok {
		return recoveredDatagram{}, false
	}

	c.mu.Lock()

	id.remote = c.identityLocked(remote)
	for index := range group.entries {
		group.entries[index].id.remote = id.remote
	}

	now := time.Now()
	stream := c.streamLocked(recoveryStreamID{remote: id.remote, ssrc: id.ssrc}, now)
	stream.enabled = true
	stream.lastTouched = now

	datagram, missing := c.recoverGroupLocked(group)
	switch len(missing) {
	case 0:
		c.mu.Unlock()

		return recoveredDatagram{}, false
	case 1:
		c.mu.Unlock()

		return datagram, datagram.addr != nil
	default:
		for _, sequenceNumber := range missing {
			if _, exists := stream.missing[sequenceNumber]; !exists {
				stream.missing[sequenceNumber] = time.Time{}
			}
		}
		c.storeGroupLocked(id, group)
		request := makeRecoveryRequest(id.ssrc, dueRepairs(stream, now))
		c.mu.Unlock()
		if len(request) > 0 {
			c.sendRequest(request, remote)
		}

		return recoveredDatagram{}, false
	}
}

func (c *recoveryConn) sendRequest(request []byte, remote net.Addr) {
	if _, err := c.UDPConn.WriteTo(request, remote); err != nil {
		c.log.Debugf("send packet recovery request: %v", err)
	}
}

func (c *recoveryConn) streamLocked(id recoveryStreamID, now time.Time) *recoveryStream {
	stream := c.streams[id]
	if stream == nil {
		if len(c.streams) >= recoveryStreamLimit {
			c.trimOldestStreamLocked()
		}
		stream = &recoveryStream{missing: make(map[uint16]time.Time), lastTouched: now}
		c.streams[id] = stream
	}

	return stream
}

func (c *recoveryConn) trackGapLocked(id recoveryPacketID, stream *recoveryStream) {
	sequenceNumber := id.sequenceNumber
	if !stream.based {
		stream.highest = sequenceNumber
		stream.based = true

		return
	}

	delta := sequenceNumber - stream.highest
	if delta == 0 || delta >= 0x8000 {
		return
	}
	// Only request packets the sender can still retain, even after a long outage.
	start := uint16(max(1, int(delta)-recoveryHistorySize+1))
	if delta > 1 {
		for missing := range stream.missing {
			if sequenceNumber-missing >= recoveryHistorySize {
				delete(stream.missing, missing)
			}
		}
	}
	for offset := start; offset < delta; offset++ {
		missing := stream.highest + offset
		if _, exists := c.packets[recoveryPacketID{
			remote: id.remote, ssrc: id.ssrc, sequenceNumber: missing,
		}]; exists {
			continue
		}
		if _, exists := stream.missing[missing]; !exists {
			stream.missing[missing] = time.Time{}
		}
	}
	stream.highest = sequenceNumber
}

func (c *recoveryConn) storePacketLocked(id recoveryPacketID, data []byte) {
	if _, exists := c.packets[id]; exists {
		return
	}

	c.packets[id] = append([]byte(nil), data...)
	c.packetOrder = append(c.packetOrder, id)
	for len(c.packets) > recoveryHistorySize {
		delete(c.packets, c.packetOrder[c.packetHead])
		c.packetHead++
	}
	if c.packetHead >= recoveryHistorySize {
		c.packetOrder = append(c.packetOrder[:0], c.packetOrder[c.packetHead:]...)
		c.packetHead = 0
	}
}

func (c *recoveryConn) recoverPendingLocked(arrived recoveryPacketID, remote net.Addr) {
	for id, group := range c.groups {
		contains := false
		for _, entry := range group.entries {
			contains = contains || entry.id == arrived
		}
		if !contains {
			continue
		}

		datagram, missing := c.recoverGroupLocked(group)
		if len(missing) <= 1 {
			delete(c.groups, id)
		}
		if len(missing) == 1 && datagram.addr != nil {
			// Deliver through the socket/path that supplied the repair. The
			// parity may have arrived on another address family or interface.
			datagram.addr = remote
			c.ready = append(c.ready, datagram)
		}
	}
}

func (c *recoveryConn) recoverGroupLocked(group recoveryGroup) (recoveredDatagram, []uint16) {
	missing := make([]uint16, 0, len(group.entries))
	for _, entry := range group.entries {
		if _, exists := c.packets[entry.id]; !exists {
			missing = append(missing, entry.id.sequenceNumber)
		}
	}
	if len(missing) != 1 {
		return recoveredDatagram{}, missing
	}

	data := append([]byte(nil), group.parity...)
	var wanted recoveryEntry
	for _, entry := range group.entries {
		packet, exists := c.packets[entry.id]
		if !exists {
			wanted = entry

			continue
		}
		if len(packet) != entry.length {
			return recoveredDatagram{}, missing
		}
		for index := range packet {
			data[index] ^= packet[index]
		}
	}
	data = data[:wanted.length]
	c.storePacketLocked(wanted.id, data)
	streamID := recoveryStreamID{remote: wanted.id.remote, ssrc: wanted.id.ssrc}
	if stream := c.streams[streamID]; stream != nil {
		delete(stream.missing, wanted.id.sequenceNumber)
	}

	return recoveredDatagram{data: data, addr: group.remote}, missing
}

func (c *recoveryConn) storeGroupLocked(id recoveryGroupID, group recoveryGroup) {
	// Completed groups leave holes in the order. Compact by order length,
	// even when the number of outstanding groups never reaches the limit.
	if len(c.groupOrder) >= 2*recoveryGroupLimit {
		order := make([]recoveryGroupID, 0, len(c.groups))
		seen := make(map[recoveryGroupID]bool, len(c.groups))
		for _, previous := range c.groupOrder[c.groupHead:] {
			if _, exists := c.groups[previous]; exists && !seen[previous] {
				order = append(order, previous)
				seen[previous] = true
			}
		}
		c.groupOrder = order
		c.groupHead = 0
	}
	if _, exists := c.groups[id]; !exists {
		c.groupOrder = append(c.groupOrder, id)
	}
	c.groups[id] = group
	for len(c.groups) > recoveryGroupLimit {
		delete(c.groups, c.groupOrder[c.groupHead])
		c.groupHead++
	}
	if c.groupHead >= recoveryGroupLimit {
		c.groupOrder = append(c.groupOrder[:0], c.groupOrder[c.groupHead:]...)
		c.groupHead = 0
	}
}

func (c *recoveryConn) identity(remote net.Addr) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.identityLocked(remote)
}

func (c *recoveryConn) identityLocked(remote net.Addr) string {
	if identity := c.identities[remote.String()]; identity != "" {
		return "ice:" + identity
	}

	return c.LocalAddr().String() + "|" + remote.String()
}

// Pion emits a binding success only after checking the ICE username and
// message integrity. An incoming username alone must never join two paths.
func (c *recoveryConn) observeBinding(data []byte, remote net.Addr, outgoing bool) {
	if !stun.IsMessage(data) {
		return
	}
	message := &stun.Message{Raw: data}
	if err := message.Decode(); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := recoveryBindingID{remote: remote.String(), transaction: message.TransactionID}
	if outgoing && message.Type == stun.BindingSuccess {
		c.confirmBindingLocked(key)

		return
	}
	if !outgoing && message.Type == stun.BindingRequest {
		c.rememberBindingLocked(key, message)
	}
}

func (c *recoveryConn) rememberBindingLocked(key recoveryBindingID, message *stun.Message) {
	var username stun.Username
	if err := username.GetFrom(message); err != nil {
		return
	}
	if c.pendingBindings == nil {
		c.pendingBindings = make(map[recoveryBindingID]string)
	}
	if len(c.pendingBindings) >= recoveryHistorySize {
		clear(c.pendingBindings)
	}
	if _, exists := c.pendingBindings[key]; !exists {
		c.pendingBindings[key] = string(username)
	}
}

func (c *recoveryConn) confirmBindingLocked(key recoveryBindingID) {
	username := c.pendingBindings[key]
	if username == "" {
		return
	}
	if c.identities == nil {
		c.identities = make(map[string]string)
	}
	if len(c.identities) >= recoveryHistorySize {
		clear(c.identities)
	}
	c.identities[key.remote] = username
	delete(c.pendingBindings, key)
}

func (c *recoveryConn) WriteTo(data []byte, remote net.Addr) (int, error) {
	c.observeBinding(data, remote, true)

	return c.UDPConn.WriteTo(data, remote)
}

func (c *recoveryConn) trimStreamsLocked(now time.Time) {
	for id, stream := range c.streams {
		if now.Sub(stream.lastTouched) > 30*time.Second {
			delete(c.streams, id)
		}
	}
}

func (c *recoveryConn) trimOldestStreamLocked() {
	var oldestID recoveryStreamID
	var oldest time.Time
	for id, stream := range c.streams {
		if oldest.IsZero() || stream.lastTouched.Before(oldest) {
			oldestID = id
			oldest = stream.lastTouched
		}
	}
	delete(c.streams, oldestID)
}

func parseRecoveryGroup(data []byte, remote net.Addr) (recoveryGroup, recoveryGroupID, bool) {
	if len(data) < recoveryHeaderSize || data[4] != recoveryParityType {
		return recoveryGroup{}, recoveryGroupID{}, false
	}

	count := int(data[5])
	entriesEnd := recoveryHeaderSize + count*4
	if count < 2 || count > 32 || len(data) < entriesEnd {
		return recoveryGroup{}, recoveryGroupID{}, false
	}

	remoteID := remote.String()
	ssrc := binary.BigEndian.Uint32(data[8:12])
	entries := make([]recoveryEntry, 0, count)
	maximumLength := 0
	for index := range count {
		offset := recoveryHeaderSize + index*4
		length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if length < 12 {
			return recoveryGroup{}, recoveryGroupID{}, false
		}
		maximumLength = max(maximumLength, length)
		entries = append(entries, recoveryEntry{
			id: recoveryPacketID{
				remote:         remoteID,
				ssrc:           ssrc,
				sequenceNumber: binary.BigEndian.Uint16(data[offset : offset+2]),
			},
			length: length,
		})
	}
	if maximumLength == 0 || len(data) != entriesEnd+maximumLength {
		return recoveryGroup{}, recoveryGroupID{}, false
	}

	group := recoveryGroup{
		entries: entries,
		parity:  append([]byte(nil), data[entriesEnd:]...),
		remote:  remote,
	}
	id := recoveryGroupID{
		remote: remoteID, ssrc: ssrc, last: entries[len(entries)-1].id.sequenceNumber,
	}

	return group, id, true
}

func recoveryRTPPacketID(data []byte) (recoveryPacketID, bool) {
	if len(data) < 12 || data[0]&0xc0 != 0x80 || data[1] >= 192 && data[1] <= 223 {
		return recoveryPacketID{}, false
	}

	return recoveryPacketID{
		ssrc:           binary.BigEndian.Uint32(data[8:12]),
		sequenceNumber: binary.BigEndian.Uint16(data[2:4]),
	}, true
}

func dueRepairs(stream *recoveryStream, now time.Time) []uint16 {
	due := make([]uint16, 0, min(len(stream.missing), recoveryRequestSize))
	for sequenceNumber, requested := range stream.missing {
		// The sender cannot repair packets outside its retained history.
		behind := stream.highest - sequenceNumber
		if stream.based && behind >= recoveryHistorySize && behind < 0x8000 {
			delete(stream.missing, sequenceNumber)

			continue
		}
		if !requested.IsZero() && now.Sub(requested) < recoveryRetry {
			continue
		}
		stream.missing[sequenceNumber] = now
		due = append(due, sequenceNumber)
		if len(due) == recoveryRequestSize {
			break
		}
	}

	return due
}

func makeRecoveryRequest(ssrc uint32, sequenceNumbers []uint16) []byte {
	if len(sequenceNumbers) == 0 {
		return nil
	}

	request := make([]byte, recoveryHeaderSize+len(sequenceNumbers)*2)
	copy(request, "WGR1")
	request[4] = recoveryRequestType
	var count byte
	for range sequenceNumbers {
		count++
	}
	request[5] = count
	binary.BigEndian.PutUint32(request[8:12], ssrc)
	for index, sequenceNumber := range sequenceNumbers {
		binary.BigEndian.PutUint16(request[recoveryHeaderSize+index*2:], sequenceNumber)
	}

	return request
}

func hasRecoveryMagic(data []byte) bool {
	return len(data) >= 4 && string(data[:4]) == "WGR1"
}

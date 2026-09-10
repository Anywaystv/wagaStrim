// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package ingest

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// ProbeSocketBuffer checks the granted size on a temporary socket because the
// mux does not expose its sockets. SetReadBuffer can succeed despite clamping
// to net.core.rmem_max (Linux) or kern.ipc.maxsockbuf (macOS).
func ProbeSocketBuffer(want int) (granted int, err error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return 0, fmt.Errorf("%w: probe socket: %w", ErrBuildAPI, err)
	}

	defer func() { _ = conn.Close() }()

	if setErr := conn.SetReadBuffer(want); setErr != nil {
		return 0, fmt.Errorf("%w: set read buffer: %w", ErrBuildAPI, setErr)
	}

	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("%w: raw conn: %w", ErrBuildAPI, err)
	}

	var (
		size    int
		sockErr error
	)

	if ctlErr := raw.Control(func(fd uintptr) {
		size, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
	}); ctlErr != nil {
		return 0, fmt.Errorf("%w: control: %w", ErrBuildAPI, ctlErr)
	}

	if sockErr != nil {
		return 0, fmt.Errorf("%w: getsockopt: %w", ErrBuildAPI, sockErr)
	}

	return size, nil
}

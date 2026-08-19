// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package ingest

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// ProbeSocketBuffer reports the receive buffer the kernel actually grants for
// the size we ask for.
//
// The kernel silently clamps to net.core.rmem_max on Linux and
// kern.ipc.maxsockbuf on macOS, and SetReadBuffer returns nil either way. A
// buffer far smaller than requested drops packets in the kernel before the
// application ever sees them, which is the single most confusing source of loss
// on a self hosted ingest: the stream looks broken and nothing logs an error.
//
// A throwaway socket is probed rather than the mux's own, because the mux does
// not expose its connections. Same kernel, same limit.
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

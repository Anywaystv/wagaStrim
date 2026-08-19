// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package reach works out the address a phone on cellular would use. It does
// not claim the forwarded ports are open, because that cannot honestly be
// determined from inside the network being tested.
package reach

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/pion/stun/v3"
)

// Public STUN servers, tried in order. Only the reflexive address is requested;
// no media and no identifying information passes through them.
var servers = []string{ //nolint:gochecknoglobals // a fixed list, not mutable state.
	"stun.l.google.com:19302",
	"stun.cloudflare.com:3478",
}

const probeTimeout = 3 * time.Second

// Report is what the UI shows about reachability.
type Report struct {
	PublicHost string   `json:"publicHost"`
	LANHosts   []string `json:"lanHosts"`
	MediaPort  int      `json:"mediaPort"`
	SignalPort int      `json:"signalPort"`
	Note       string   `json:"note"`
	Err        string   `json:"error,omitempty"`
}

// Look discovers the public address and reports plainly what it did not check.
func Look(ctx context.Context, mediaPort, signalPort int) Report {
	report := Report{
		MediaPort:  mediaPort,
		SignalPort: signalPort,
		LANHosts:   lanAddresses(),
		// Saying "ports open" here would be a lie. A probe sent from inside the
		// network can traverse NAT loopback and succeed against a port the
		// outside world cannot reach, so a green tick would be worse than
		// nothing: it would send someone hunting the wrong fault.
		Note: "Public address found. Whether the two ports are forwarded cannot be " +
			"checked from inside your own network, because a router can answer its own " +
			"probe. The real test is your phone on cellular with Wi-Fi turned off.",
	}

	host, err := discover(ctx)
	if err != nil {
		report.Err = err.Error()
		report.Note = "No public address yet. Streaming over the internet needs one; " +
			"a phone on the same Wi-Fi can use a LAN address below."

		return report
	}

	report.PublicHost = host

	return report
}

// discover asks each STUN server in turn for our reflexive address.
func discover(ctx context.Context) (string, error) {
	var last error

	for _, server := range servers {
		host, err := ask(ctx, server)
		if err != nil {
			last = err

			continue
		}

		return host, nil
	}

	if last == nil {
		last = ErrNoSTUN
	}

	return "", fmt.Errorf("%w: %w", ErrNoSTUN, last)
}

func ask(ctx context.Context, server string) (string, error) {
	probe, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var dialer net.Dialer

	conn, err := dialer.DialContext(probe, "udp4", server)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrNoSTUN, server, err)
	}

	client, err := stun.NewClient(conn)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrNoSTUN, server, err)
	}

	defer func() { _ = client.Close() }()

	var (
		mapped stun.XORMappedAddress
		inner  error
	)

	err = client.Do(stun.MustBuild(stun.TransactionID, stun.BindingRequest), func(ev stun.Event) {
		if ev.Error != nil {
			inner = ev.Error

			return
		}

		inner = mapped.GetFrom(ev.Message)
	})
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrNoSTUN, server, err)
	}

	if inner != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrNoAddress, server, inner)
	}

	return mapped.IP.String(), nil
}

// lanAddresses lists routable local addresses, for testing on the same network
// before anything is forwarded.
func lanAddresses() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}

	hosts := make([]string, 0, len(addrs))

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
			continue
		}

		if ipNet.IP.To4() != nil {
			hosts = append(hosts, ipNet.IP.String())
		}
	}

	return hosts
}

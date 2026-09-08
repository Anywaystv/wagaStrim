// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func (c *Config) LANControlEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.ControlLAN == nil || *c.ControlLAN
}

func (c *Config) SetLANControl(on bool) error {
	return c.SetControlAccess("lan", on)
}

func (c *Config) RemoteControlEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.ControlRemote != nil {
		return *c.ControlRemote
	}

	// Existing hosted deployments configure only a token and rely on remote control.
	return c.ControlToken != ""
}

func (c *Config) SetControlAccess(scope string, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previousLAN, previousRemote := c.ControlLAN, c.ControlRemote
	switch scope {
	case "lan":
		c.ControlLAN = &on
	case "remote":
		c.ControlRemote = &on
	default:
		return fmt.Errorf("%w: unknown control access option", ErrParseConfig)
	}
	if err := c.saveLocked(); err != nil {
		c.ControlLAN, c.ControlRemote = previousLAN, previousRemote

		return err
	}

	return nil
}

func (c *Config) ValidateTransport() error {
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return fmt.Errorf("%w: tlsCert and tlsKey must be set together", ErrParseConfig)
	}
	if c.PublicURL != "" && !httpsOrigin(c.PublicURL) {
		return fmt.Errorf("%w: publicUrl must be an HTTPS origin", ErrParseConfig)
	}

	return nil
}

func httpsOrigin(value string) bool {
	u, err := url.Parse(value)
	if err != nil {
		return false
	}

	return u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" &&
		u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

func (c *Config) SignalLinks(host string) (string, string) {
	if c.PublicURL != "" {
		base := strings.TrimSuffix(c.PublicURL, "/")

		return "whips" + strings.TrimPrefix(base, "https") + "/whip/", base + "/player/"
	}
	sender, receiver := "whip", "http"
	if c.TLSCert != "" && c.TLSKey != "" {
		sender, receiver = "whips", "https"
	}
	authority := net.JoinHostPort(strings.Trim(host, "[]"), fmt.Sprint(c.SignalPort))

	return sender + "://" + authority + "/whip/", receiver + "://" + authority + "/player/"
}

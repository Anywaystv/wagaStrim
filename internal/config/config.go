// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package config holds the on-disk settings and the ingest list. The file is the
// only mutable state the daemon keeps; everything else is derived at startup.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Delay bounds in milliseconds. The floor is not a default, it is a floor: two
// seconds of buffered media is what keeps OBS fed through a tunnel or a tower
// handoff. A value below it is clamped rather than honored, and the file is
// never trusted to stay inside the range.
const (
	DelayFloorMS   = 2000
	DelayDefaultMS = 2000
	DelayMaxMS     = 10000
)

// Version is the schema version written to new files.
const Version = 1

// Default ports. Media and signaling are forwarded; the UI never leaves loopback.
const (
	DefaultMediaPort  = 7332
	DefaultSignalPort = 7331
	DefaultUIPort     = 7330
)

// Codec identifiers as they appear in the config file and the UI.
const (
	CodecH264 = "h264"
	CodecH265 = "h265"
	CodecAV1  = "av1"
)

// Ingest is one camera: a link pair, a codec set, and a playout target.
type Ingest struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	SenderKey   string   `json:"senderKey"`
	ReceiverKey string   `json:"receiverKey"`
	Codecs      []string `json:"codecs"`
	DelayMS     int      `json:"delayMs"`
	SyncGroup   string   `json:"syncGroup,omitempty"`
}

// Config is the whole persisted state.
type Config struct {
	Version    int      `json:"version"`
	PublicHost string   `json:"publicHost"`
	MediaPort  int      `json:"mediaPort"`
	SignalPort int      `json:"signalPort"`
	UIPort     int      `json:"uiPort"`
	Autostart  bool     `json:"autostart"`
	Ingests    []Ingest `json:"ingests"`

	path string
}

// Path returns the file this config was loaded from.
func (c *Config) Path() string {
	return c.path
}

// Load reads the config, creating it with defaults when absent. Values outside
// their permitted range are corrected in memory and written back on the next
// save, so hand-editing the file cannot put the daemon into an invalid state.
func Load() (*Config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLocateConfig, err)
	}

	path := filepath.Join(dir, "wagastrim", "config.json")

	cfg := &Config{
		Version:    Version,
		MediaPort:  DefaultMediaPort,
		SignalPort: DefaultSignalPort,
		UIPort:     DefaultUIPort,
		path:       path,
	}

	raw, err := os.ReadFile(path) //nolint:gosec // path is derived from os.UserConfigDir.
	if os.IsNotExist(err) {
		return cfg, cfg.Save()
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReadConfig, err)
	}

	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseConfig, err)
	}

	if cfg.Version > Version {
		return nil, fmt.Errorf("%w: file is v%d, this build understands v%d",
			ErrFutureConfig, cfg.Version, Version)
	}

	cfg.path = path
	cfg.normalise()

	return cfg, nil
}

// normalise clamps every field the file could have carried out of range.
func (c *Config) normalise() {
	c.Version = Version

	if c.MediaPort == 0 {
		c.MediaPort = DefaultMediaPort
	}

	if c.SignalPort == 0 {
		c.SignalPort = DefaultSignalPort
	}

	if c.UIPort == 0 {
		c.UIPort = DefaultUIPort
	}

	for idx := range c.Ingests {
		ing := &c.Ingests[idx]
		ing.DelayMS = clampDelay(ing.DelayMS)

		if len(ing.Codecs) == 0 {
			ing.Codecs = []string{CodecH264}
		}
	}
}

// clampDelay holds a playout target inside the permitted range. The floor is the
// product, not a preference, so it is applied on every path that can set a delay.
func clampDelay(delayMS int) int {
	switch {
	case delayMS < DelayFloorMS:
		return DelayFloorMS
	case delayMS > DelayMaxMS:
		return DelayMaxMS
	default:
		return delayMS
	}
}

// Save writes the config through a temporary file and a rename, so a crash
// mid-write cannot leave a truncated file behind.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	return nil
}

// AddIngest appends a camera with a fresh key pair and saves.
func (c *Config) AddIngest(label string) (*Ingest, error) {
	senderKey, err := newKey(SenderPrefix)
	if err != nil {
		return nil, err
	}

	receiverKey, err := newKey(ReceiverPrefix)
	if err != nil {
		return nil, err
	}

	id, err := newKey("")
	if err != nil {
		return nil, err
	}

	c.Ingests = append(c.Ingests, Ingest{
		ID:          id,
		Label:       label,
		SenderKey:   senderKey,
		ReceiverKey: receiverKey,
		Codecs:      []string{CodecH264},
		DelayMS:     DelayDefaultMS,
	})

	return &c.Ingests[len(c.Ingests)-1], c.Save()
}

// RemoveIngest drops a camera, revoking both of its keys.
func (c *Config) RemoveIngest(id string) error {
	for idx := range c.Ingests {
		if c.Ingests[idx].ID != id {
			continue
		}

		c.Ingests = append(c.Ingests[:idx], c.Ingests[idx+1:]...)

		return c.Save()
	}

	return fmt.Errorf("%w: %s", ErrUnknownIngest, id)
}

// SetDelay clamps to the permitted range and saves.
func (c *Config) SetDelay(id string, delayMS int) error {
	for idx := range c.Ingests {
		if c.Ingests[idx].ID != id {
			continue
		}

		c.Ingests[idx].DelayMS = clampDelay(delayMS)

		return c.Save()
	}

	return fmt.Errorf("%w: %s", ErrUnknownIngest, id)
}

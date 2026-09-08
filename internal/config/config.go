// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package config holds the on-disk settings and the ingest list. The file is the
// only mutable state the daemon keeps; everything else is derived at startup.
package config

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Delay bounds are independent of the initial buffer for a new camera.
const (
	DelayFloorMS   = 0
	DelayDefaultMS = 2000
	DelayMaxMS     = 10000
)

// Version is the schema version written to new files.
const Version = 1

// Default ports. Media and signaling are forwarded; the UI never leaves loopback.
// Control binds only where a deployment configured a token, and belongs behind a
// firewall rule as well as behind that token.
const (
	DefaultMediaPort   = 7332
	DefaultSignalPort  = 7331
	DefaultUIPort      = 7330
	DefaultControlPort = 7333
)

// Codec identifiers as they appear in the config file and the UI.
const (
	CodecH264 = "h264"
	CodecH265 = "h265"
	CodecAV1  = "av1"
)

// AllCodecs is every video codec the relay can carry, in preference order.
func AllCodecs() []string {
	return []string{CodecH264, CodecH265, CodecAV1}
}

// CodecLabelOf is how a negotiated RTP mime type is written for a person, so
// what a phone actually picked can be reported without the config package
// taking a dependency on the media stack. An empty return is a codec the relay
// never registered, which no negotiation can settle on.
func CodecLabelOf(mimeType string) string {
	for _, name := range AllCodecs() {
		if strings.EqualFold(mimeType, "video/"+name) {
			return CodecLabel(name)
		}
	}

	return ""
}

// CodecLabel is how one of the identifiers above is written for a person.
func CodecLabel(name string) string {
	switch name {
	case CodecH264:
		return "H.264"
	case CodecH265:
		return "H.265"
	case CodecAV1:
		return "AV1"
	default:
		return ""
	}
}

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
	Version    int    `json:"version"`
	PublicHost string `json:"publicHost"`
	// Bind addresses can restrict signaling and control to a LAN or VPN interface.
	SignalBind    string `json:"signalBind,omitempty"`
	ControlBind   string `json:"controlBind,omitempty"`
	ControlLAN    *bool  `json:"controlLan,omitempty"`
	ControlRemote bool   `json:"controlRemote,omitempty"`
	TLSCert       string `json:"tlsCert,omitempty"`
	TLSKey        string `json:"tlsKey,omitempty"`
	// PublicURL is the HTTPS origin of a local reverse proxy.
	PublicURL string `json:"publicUrl,omitempty"`
	// ICEPublicIPs are addresses reached through a 1:1 UDP port forward.
	// Legacy public/local entries are accepted, but only the public half is
	// advertised. Local candidates remain available for nearby subscribers.
	ICEPublicIPs []string `json:"icePublicIPs,omitempty"`
	MediaPort    int      `json:"mediaPort"`
	SignalPort   int      `json:"signalPort"`
	UIPort       int      `json:"uiPort"`
	Autostart    bool     `json:"autostart"`

	// ControlToken turns the control listener on. Empty means a deployment owns
	// nothing here and the listener never binds, which is every desktop install.
	ControlToken string `json:"controlToken,omitempty"`
	ControlPort  int    `json:"controlPort,omitempty"`

	// FloorMS lets deployments impose a higher minimum. Unset allows zero delay.
	FloorMS *int     `json:"delayFloorMs,omitempty"`
	Ingests []Ingest `json:"ingests"`

	// The settings page mutates this while the public signaling listener reads
	// it, on separate listeners and separate goroutines.
	mu   sync.RWMutex
	path string
}

// SetPublicHost records a discovered address. The reachability handler writes
// this while the page renderer and the link builder are reading it.
func (c *Config) SetPublicHost(host string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.PublicHost == host {
		return nil
	}

	c.PublicHost = host

	return c.saveLocked()
}

// SetAutostart records the toggle. The platform is the authority on whether the
// entry exists; this is only what the page shows before it asks.
func (c *Config) SetAutostart(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Autostart = on

	return c.saveLocked()
}

// Host returns the public address, or an empty string if none is known yet.
func (c *Config) Host() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.PublicHost
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

	if c.ControlPort == 0 {
		c.ControlPort = DefaultControlPort
	}

	if c.FloorMS != nil {
		floor := clampFloor(c.FloorMS)
		c.FloorMS = &floor
	}

	for idx := range c.Ingests {
		ing := &c.Ingests[idx]
		ing.DelayMS = c.clampDelay(ing.DelayMS)

		ing.Codecs = keepCodecs(ing.Codecs)
	}
}

// clampFloor retains explicit deployment limits within the supported range.
func clampFloor(floorMS *int) int {
	switch {
	case floorMS == nil:
		return DelayFloorMS
	case *floorMS < 0:
		return 0
	case *floorMS > DelayMaxMS:
		return DelayMaxMS
	default:
		return *floorMS
	}
}

// clampDelay applies the same bounds to loaded settings and API updates.
func (c *Config) clampDelay(delayMS int) int {
	floor := clampFloor(c.FloorMS)

	switch {
	case delayMS < floor:
		return floor
	case delayMS > DelayMaxMS:
		return DelayMaxMS
	default:
		return delayMS
	}
}

// Save writes the config through a temporary file and a rename, so a crash
// mid-write cannot leave a truncated file behind.
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.saveLocked()
}

// saveLocked is Save for callers that already hold the write lock.
func (c *Config) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	file, err := os.CreateTemp(filepath.Dir(c.path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}
	tmp := file.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = file.Write(append(raw, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, closeErr)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	return nil
}

// AddIngest appends a camera with a fresh key pair and saves.
func (c *Config) AddIngest(label string) (Ingest, error) {
	senderKey, err := newKey(SenderPrefix)
	if err != nil {
		return Ingest{}, err
	}

	receiverKey, err := newKey(ReceiverPrefix)
	if err != nil {
		return Ingest{}, err
	}

	id, err := newKey("")
	if err != nil {
		return Ingest{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.Ingests = append(c.Ingests, Ingest{
		ID:          id,
		Label:       label,
		SenderKey:   senderKey,
		ReceiverKey: receiverKey,
		Codecs:      []string{CodecH264},
		DelayMS:     c.clampDelay(DelayDefaultMS),
	})

	return c.Ingests[len(c.Ingests)-1], c.saveLocked()
}

// ReplaceIngests sets the whole list from a deployment that owns it elsewhere.
// A compositor box is cattle: it is deleted and recreated on an idle timer, and
// a key minted here would hand the streamer a new push URL every time. The
// dashboard keeps the list and pushes it, so a replaced machine comes back with
// the same links.
//
// The returned identifiers are the cameras whose live sessions can no longer be
// trusted, either because the camera is gone or because its keys or codecs
// moved under it. The caller closes those, exactly as removing one from the
// settings page does.
func (c *Config) ReplaceIngests(next []Ingest) ([]string, error) {
	if err := validateIngests(next); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	stale := staleIngests(c.Ingests, next)
	replacement := make([]Ingest, len(next))

	for idx := range next {
		ing := next[idx]
		ing.Codecs = keepCodecs(ing.Codecs)
		ing.DelayMS = c.clampDelay(ing.DelayMS)
		replacement[idx] = ing
	}

	c.Ingests = replacement

	return stale, c.saveLocked()
}

// validateIngests rejects a list that could not be resolved unambiguously.
func validateIngests(list []Ingest) error {
	seen := make(map[string]struct{}, len(list)*3)

	for idx := range list {
		ing := list[idx]

		if ing.ID == "" {
			return fmt.Errorf("%w: position %d", ErrBadIngest, idx)
		}

		if _, repeated := seen[ing.ID]; repeated {
			return fmt.Errorf("%w: %s", ErrBadIngest, ing.ID)
		}

		seen[ing.ID] = struct{}{}

		if !validKey(SenderPrefix, ing.SenderKey) || !validKey(ReceiverPrefix, ing.ReceiverKey) {
			return fmt.Errorf("%w: on ingest %s", ErrBadKey, ing.ID)
		}

		for _, key := range []string{ing.SenderKey, ing.ReceiverKey} {
			if _, repeated := seen[key]; repeated {
				return fmt.Errorf("%w: on ingest %s", ErrDuplicateKey, ing.ID)
			}

			seen[key] = struct{}{}
		}
	}

	return nil
}

// staleIngests names the cameras whose running session cannot survive the new
// list. A publisher holding a key that no longer resolves would keep sending
// into a camera nobody can subscribe to, and a codec change needs the
// negotiation redone.
func staleIngests(current, next []Ingest) []string {
	stale := make([]string, 0, len(current))

	for idx := range current {
		was := current[idx]
		found := false

		for jdx := range next {
			now := next[jdx]
			if now.ID != was.ID {
				continue
			}

			found = true

			if now.SenderKey != was.SenderKey || now.ReceiverKey != was.ReceiverKey ||
				!slices.Equal(keepCodecs(now.Codecs), was.Codecs) {
				stale = append(stale, was.ID)
			}

			break
		}

		if !found {
			stale = append(stale, was.ID)
		}
	}

	return stale
}

// RemoveIngest drops a camera, revoking both of its keys.
func (c *Config) RemoveIngest(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for idx := range c.Ingests {
		if c.Ingests[idx].ID != id {
			continue
		}

		c.Ingests = append(c.Ingests[:idx], c.Ingests[idx+1:]...)

		return c.saveLocked()
	}

	return fmt.Errorf("%w: %s", ErrUnknownIngest, id)
}

// EffectiveDelay is the target a camera actually plays out at. Members of a sync
// group share one target, the largest any member needs, so two cameras on
// screen together do not drift apart. The worst path sets the pace, which is the
// only way they stay aligned.
func (c *Config) EffectiveDelay(id string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	own, group := 0, ""

	for idx := range c.Ingests {
		if c.Ingests[idx].ID == id {
			own, group = c.Ingests[idx].DelayMS, c.Ingests[idx].SyncGroup

			break
		}
	}

	if group == "" {
		return own
	}

	for idx := range c.Ingests {
		if c.Ingests[idx].SyncGroup == group && c.Ingests[idx].DelayMS > own {
			own = c.Ingests[idx].DelayMS
		}
	}

	return own
}

// GroupPeers lists every camera sharing a target with this one, itself included.
// Changing one member's delay has to retarget the rest.
func (c *Config) GroupPeers(id string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	group := ""

	for idx := range c.Ingests {
		if c.Ingests[idx].ID == id {
			group = c.Ingests[idx].SyncGroup

			break
		}
	}

	if group == "" {
		return []string{id}
	}

	peers := make([]string, 0, len(c.Ingests))

	for idx := range c.Ingests {
		if c.Ingests[idx].SyncGroup == group {
			peers = append(peers, c.Ingests[idx].ID)
		}
	}

	return peers
}

// update applies a change to one camera under the write lock and saves. Every
// setter was the same find-by-id loop around one assignment.
func (c *Config) update(id string, change func(*Ingest)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for idx := range c.Ingests {
		if c.Ingests[idx].ID != id {
			continue
		}

		change(&c.Ingests[idx])

		return c.saveLocked()
	}

	return fmt.Errorf("%w: %s", ErrUnknownIngest, id)
}

// SetSyncGroup moves a camera into a named group, or out of one when empty.
func (c *Config) SetSyncGroup(id, group string) error {
	return c.update(id, func(ing *Ingest) { ing.SyncGroup = group })
}

// SetLabel renames a camera. The label is how someone tells four masked links
// apart, so it has to be editable after the fact.
func (c *Config) SetLabel(id, label string) error {
	return c.update(id, func(ing *Ingest) { ing.Label = label })
}

// SetCodecs records which video codecs a camera offers. An empty set would
// negotiate nothing, so H.264 is restored rather than leaving a dead camera:
// every phone can send it, which makes it the only safe fallback.
func (c *Config) SetCodecs(id string, codecs []string) error {
	keep := keepCodecs(codecs)

	return c.update(id, func(ing *Ingest) { ing.Codecs = keep })
}

// keepCodecs narrows a requested set to the codecs the relay can carry, in
// preference order, and never returns an empty set.
func keepCodecs(codecs []string) []string {
	keep := make([]string, 0, len(codecs))

	for _, name := range AllCodecs() {
		if slices.Contains(codecs, name) {
			keep = append(keep, name)
		}
	}

	if len(keep) == 0 {
		keep = []string{CodecH264}
	}

	return keep
}

// SetDelay clamps to the permitted range and saves.
func (c *Config) SetDelay(id string, delayMS int) error {
	return c.update(id, func(ing *Ingest) { ing.DelayMS = c.clampDelay(delayMS) })
}

// Role says which half of an ingest a key belongs to.
type Role int

// Key roles. RoleNone means the key matched nothing.
const (
	RoleNone Role = iota
	RoleSender
	RoleReceiver
)

// Resolve finds the ingest a key belongs to and which half it is. Every ingest
// is compared even after a match so the work does not depend on the secret, and
// each comparison is constant time.
func (c *Config) Resolve(key string) (Ingest, Role) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var (
		found Ingest
		role  = RoleNone
	)

	for idx := range c.Ingests {
		ing := c.Ingests[idx]

		if constantEqual(key, ing.SenderKey) {
			found, role = ing, RoleSender
		}

		if constantEqual(key, ing.ReceiverKey) {
			found, role = ing, RoleReceiver
		}
	}

	return found, role
}

// List returns a copy of the ingest list. Callers iterate the copy, so a camera
// added while a page renders cannot move the slice underneath them.
func (c *Config) List() []Ingest {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]Ingest, len(c.Ingests))
	copy(out, c.Ingests)

	return out
}

// Floor is the lowest delay this deployment permits, which the settings page
// needs to render a slider that cannot ask for a value it would clamp.
func (c *Config) Floor() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return clampFloor(c.FloorMS)
}

// IngestIDs lists every camera identifier, which is what a stats reader needs
// and the only part of the list it may see.
func (c *Config) IngestIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]string, len(c.Ingests))

	for idx := range c.Ingests {
		out[idx] = c.Ingests[idx].ID
	}

	return out
}

func constantEqual(lhs, rhs string) bool {
	return subtle.ConstantTimeCompare([]byte(lhs), []byte(rhs)) == 1
}

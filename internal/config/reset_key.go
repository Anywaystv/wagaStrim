// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import "fmt"

// ResetSenderKey preserves viewer links and commits the replacement before callers revoke sessions.
func (c *Config) ResetSenderKey(id string) (Ingest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for idx := range c.Ingests {
		if c.Ingests[idx].ID != id {
			continue
		}
		key, err := newKey(SenderPrefix)
		if err != nil {
			return Ingest{}, err
		}
		previous := c.Ingests[idx].SenderKey
		c.Ingests[idx].SenderKey = key
		if err := c.saveLocked(); err != nil {
			c.Ingests[idx].SenderKey = previous

			return Ingest{}, err
		}

		return c.Ingests[idx], nil
	}

	return Ingest{}, fmt.Errorf("%w: %s", ErrUnknownIngest, id)
}

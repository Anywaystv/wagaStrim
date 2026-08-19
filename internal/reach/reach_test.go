// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package reach

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The note is the feature. A green tick would be a lie, because a probe from
// inside the network can traverse NAT loopback and pass against a closed port.
func TestReportNeverClaimsPortsAreOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	report := Look(ctx, 7332, 7331)

	assert.NotContains(t, report.Note, "open")
	assert.Equal(t, 7332, report.MediaPort)
	assert.Equal(t, 7331, report.SignalPort)
}

func TestAFailedLookupStillAdvises(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	report := Look(ctx, 7332, 7331)

	if report.PublicHost == "" {
		assert.NotEmpty(t, report.Note, "a failure must still tell the user what to do")
		assert.NotEmpty(t, report.Err)
	}
}

func TestLANAddressesExcludeLoopback(t *testing.T) {
	for _, host := range lanAddresses() {
		assert.NotEqual(t, "127.0.0.1", host)
	}
}

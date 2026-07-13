// Package clock implements the startup clock-skew check.
package clock

import (
	"context"
	"fmt"
	"time"

	"github.com/beevik/ntp"
)

const (
	Tolerance  = 100 * time.Millisecond
	ntpServer  = "pool.ntp.org"
	ntpTimeout = 4 * time.Second
)

// queryNTP is overridable in tests.
var queryNTP = func(host string, timeout time.Duration) (*ntp.Response, error) {
	return ntp.QueryWithOptions(host, ntp.QueryOptions{Timeout: timeout})
}

// Check queries an NTP server and compares its estimate of the local clock's
// offset against Tolerance. Returns an error if drift exceeds the tolerance.
func Check(ctx context.Context) error {
	timeout := ntpTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	resp, err := queryNTP(ntpServer, timeout)
	if err != nil {
		return fmt.Errorf("ntp query %s: %w", ntpServer, err)
	}
	if err := resp.Validate(); err != nil {
		return fmt.Errorf("ntp response invalid: %w", err)
	}
	return evaluateDrift(resp.ClockOffset)
}

// evaluateDrift returns nil if |offset| <= Tolerance.
func evaluateDrift(offset time.Duration) error {
	drift := offset
	if drift < 0 {
		drift = -drift
	}
	if drift > Tolerance {
		return fmt.Errorf("clock drift %v exceeds %v (offset=%v)", drift, Tolerance, offset)
	}
	return nil
}

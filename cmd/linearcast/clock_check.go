package main

import (
	"context"
	"log"

	"github.com/tckrcr/linearcast/internal/clock"
)

func runStartupClockCheck(ctx context.Context, mode string) error {
	if mode == clockCheckDisabled {
		log.Printf("ntp clock check disabled by LINEARCAST_CLOCK_CHECK=%s", clockCheckDisabled)
		return nil
	}
	if err := clock.Check(ctx); err != nil {
		return err
	}
	log.Printf("ntp drift within tolerance")
	return nil
}

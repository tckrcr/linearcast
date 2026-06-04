package clock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/beevik/ntp"
)

func TestEvaluateDriftZero(t *testing.T) {
	if err := evaluateDrift(0); err != nil {
		t.Errorf("unexpected err: %v", err)
	}
}

func TestEvaluateDriftAtEdge(t *testing.T) {
	if err := evaluateDrift(Tolerance); err != nil {
		t.Errorf("drift at boundary should pass: %v", err)
	}
	if err := evaluateDrift(-Tolerance); err != nil {
		t.Errorf("negative drift at boundary should pass: %v", err)
	}
}

func TestEvaluateDriftExceeded(t *testing.T) {
	if err := evaluateDrift(Tolerance + time.Millisecond); err == nil {
		t.Error("expected drift error")
	}
	if err := evaluateDrift(-(Tolerance + time.Millisecond)); err == nil {
		t.Error("expected drift error for negative offset")
	}
}

func TestCheckUsesNTPOffset(t *testing.T) {
	orig := queryNTP
	t.Cleanup(func() { queryNTP = orig })

	queryNTP = func(_ string, _ time.Duration) (*ntp.Response, error) {
		return &ntp.Response{
			ClockOffset:    50 * time.Millisecond,
			Stratum:        2,
			RootDelay:      10 * time.Millisecond,
			RootDispersion: 10 * time.Millisecond,
		}, nil
	}
	if err := Check(context.Background()); err != nil {
		t.Errorf("Check should succeed with 50ms offset: %v", err)
	}

	queryNTP = func(_ string, _ time.Duration) (*ntp.Response, error) {
		return &ntp.Response{
			ClockOffset:    250 * time.Millisecond,
			Stratum:        2,
			RootDelay:      10 * time.Millisecond,
			RootDispersion: 10 * time.Millisecond,
		}, nil
	}
	if err := Check(context.Background()); err == nil {
		t.Error("Check should fail with 250ms offset")
	}
}

func TestCheckPropagatesQueryError(t *testing.T) {
	orig := queryNTP
	t.Cleanup(func() { queryNTP = orig })

	sentinel := errors.New("boom")
	queryNTP = func(_ string, _ time.Duration) (*ntp.Response, error) {
		return nil, sentinel
	}
	err := Check(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel error, got %v", err)
	}
}

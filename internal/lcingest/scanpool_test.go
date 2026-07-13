package lcingest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// countingReporter satisfies Logger + ProgressReporter for pool tests.
type countingReporter struct {
	progress atomic.Int32
}

func (c *countingReporter) Printf(string, ...any) {}

func (c *countingReporter) OnProgress() {
	c.progress.Add(1)
}

func TestScanPoolProcessesAllTasksAndMergesResults(t *testing.T) {
	tasks := make([]int, 100)
	for i := range tasks {
		tasks[i] = i
	}
	rep := &countingReporter{}
	res := ScanPool(context.Background(), tasks, rep, func(n int, r *Result) {
		r.Total++
		if n%2 == 0 {
			r.Passed++
		} else {
			r.recordFailure("odd")
		}
	})
	if res.Total != 100 || res.Passed != 50 || res.Failed != 50 {
		t.Fatalf("Result = %+v, want Total=100 Passed=50 Failed=50", res)
	}
	if got := res.FailureReasons["odd"]; got != 50 {
		t.Fatalf("FailureReasons[odd] = %d, want 50", got)
	}
	if got := rep.progress.Load(); got != 100 {
		t.Fatalf("OnProgress fired %d times, want 100", got)
	}
}

func TestScanPoolRunsConcurrently(t *testing.T) {
	workers := ScanConcurrency()
	if workers < 2 {
		t.Skipf("ScanConcurrency() = %d, need at least 2", workers)
	}
	// Two tasks rendezvous: each blocks until the other has started, which
	// only resolves when tasks run on separate workers at the same time.
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	ScanPool(context.Background(), []int{1, 2}, nil, func(int, *Result) {
		started <- struct{}{}
		once.Do(func() {
			<-started
			<-started
			close(release)
		})
		<-release
	})
}

func TestScanPoolCancellationSkipsRemainingTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tasks := make([]int, 50)
	var attempted atomic.Int32
	res := ScanPool(ctx, tasks, nil, func(_ int, r *Result) {
		r.Total++
		attempted.Add(1)
		cancel()
	})
	if got := attempted.Load(); int(got) != res.Total {
		t.Fatalf("attempted %d tasks but Result.Total = %d", got, res.Total)
	}
	if res.Total >= 50 {
		t.Fatalf("Result.Total = %d, want fewer than 50 after cancellation", res.Total)
	}
}

func TestScanPoolEmptyTasks(t *testing.T) {
	res := ScanPool(context.Background(), nil, nil, func(int, *Result) {
		t.Fatal("work must not run with no tasks")
	})
	if res.Total != 0 {
		t.Fatalf("Result.Total = %d, want 0", res.Total)
	}
}

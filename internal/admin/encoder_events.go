package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type encoderProgressEvent struct {
	PackageID      string `json:"packageId"`
	ProgressPct    *int   `json:"progressPct,omitempty"`
	LeaseExpiresMs int64  `json:"leaseExpiresMs"`
}

// encoderBroadcaster fans out encoder progress events to connected SSE clients.
// Slow consumers receive a dropped event rather than blocking the heartbeat handler.
type encoderBroadcaster struct {
	mu   sync.Mutex
	subs map[chan encoderProgressEvent]struct{}
}

func newEncoderBroadcaster() *encoderBroadcaster {
	return &encoderBroadcaster{
		subs: make(map[chan encoderProgressEvent]struct{}),
	}
}

func (b *encoderBroadcaster) subscribe() chan encoderProgressEvent {
	ch := make(chan encoderProgressEvent, 8)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *encoderBroadcaster) unsubscribe(ch chan encoderProgressEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (b *encoderBroadcaster) publish(ev encoderProgressEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// handleEncoderEvents streams encoder progress as Server-Sent Events.
// Each event carries the packageId, progressPct, and leaseExpiresMs from the
// encoder's most recent heartbeat. A comment ping is sent every 30s to keep
// the connection alive through proxies.
func (a *App) handleEncoderEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	_ = rc.Flush()

	ch := a.encoderBroadcaster.subscribe()
	defer a.encoderBroadcaster.unsubscribe(ch)

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprintf(w, ": ping\n\n")
			_ = rc.Flush()
		case ev := <-ch:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			_ = rc.Flush()
		}
	}
}

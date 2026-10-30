package worker

import (
	"log"
	"sync"
	"time"
)

// maxLocalHold caps how long a locally parsed reset holds the shem. It mirrors
// the server default for supervisor.usage_limit_max, and syncLimit re-learns the
// server's own value on the next poll.
const maxLocalHold = 8 * time.Hour

// limitBreaker holds each backend's "usable again at" time. The clock is
// injectable. It is only a cache: the server keeps the shem's limit
// on its row, so a restarted shem re-learns it on its next poll.
type limitBreaker struct {
	mu    sync.Mutex
	now   func() time.Time
	until map[string]time.Time
}

func newLimitBreaker() *limitBreaker {
	return &limitBreaker{now: time.Now, until: map[string]time.Time{}}
}

// trip records that backend is limited until t, keeping the later time, and
// logs once per extension.
func (b *limitBreaker) trip(backend string, t time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !t.After(b.now()) || !t.After(b.until[backend]) {
		return
	}
	b.until[backend] = t
	log.Printf("worker: backend %q limited until %s; not claiming new work", backend, t.Format(time.RFC3339))
}

// limited reports whether backend is held right now.
func (b *limitBreaker) limited(backend string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.until[backend].After(b.now())
}

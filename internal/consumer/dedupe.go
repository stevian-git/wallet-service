package consumer

import (
	"sync"
	"time"
)

type Dedupe struct {
	mu          sync.Mutex
	seen        map[string]time.Time
	order       []seenEntry // insertion order == seenAt order; may hold stale entries
	ttl         time.Duration
	maxEntries  int
	lastCleanup time.Time
}

type seenEntry struct {
	id string
	at time.Time
}

func NewDedupe(ttl time.Duration, maxEntries int) *Dedupe {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if maxEntries <= 0 {
		maxEntries = 100_000
	}
	return &Dedupe{
		seen:        make(map[string]time.Time),
		ttl:         ttl,
		maxEntries:  maxEntries,
		lastCleanup: time.Now(),
	}
}

func (d *Dedupe) Seen(eventID string) bool {
	if eventID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanupLocked(time.Now())
	_, ok := d.seen[eventID]
	return ok
}

func (d *Dedupe) Add(eventID string) {
	if eventID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Read the clock under the lock so d.order stays sorted by time.
	now := time.Now()
	d.cleanupLocked(now)
	d.seen[eventID] = now
	d.order = append(d.order, seenEntry{id: eventID, at: now})
	for len(d.seen) > d.maxEntries {
		d.popOldestLocked()
	}
}

func (d *Dedupe) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

// cleanupLocked drops entries older than ttl. It runs at most once per ttl,
// and because d.order is time-sorted it only touches the expired prefix.
func (d *Dedupe) cleanupLocked(now time.Time) {
	if now.Sub(d.lastCleanup) < d.ttl {
		return
	}
	cutoff := now.Add(-d.ttl)
	for len(d.order) > 0 && d.order[0].at.Before(cutoff) {
		d.popOldestLocked()
	}
	d.lastCleanup = now
}

// popOldestLocked removes the front of d.order, deleting it from the map only
// if the map still holds that exact sighting (a re-Add makes older ones stale).
func (d *Dedupe) popOldestLocked() {
	oldest := d.order[0]
	d.order = d.order[1:]
	if at, ok := d.seen[oldest.id]; ok && at.Equal(oldest.at) {
		delete(d.seen, oldest.id)
	}
}

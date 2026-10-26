package consumer

import (
	"sync"
	"time"
)

type Dedupe struct {
	mu          sync.Mutex
	seen        map[string]time.Time
	ttl         time.Duration
	maxEntries  int
	lastCleanup time.Time
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
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanupLocked(now)
	d.seen[eventID] = now
	d.enforceCapLocked()
}

func (d *Dedupe) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

func (d *Dedupe) cleanupLocked(now time.Time) {
	if now.Sub(d.lastCleanup) < d.ttl {
		return
	}
	cutoff := now.Add(-d.ttl)
	for id, seenAt := range d.seen {
		if seenAt.Before(cutoff) {
			delete(d.seen, id)
		}
	}
	d.lastCleanup = now
}

func (d *Dedupe) enforceCapLocked() {
	for len(d.seen) > d.maxEntries {
		var (
			oldestID   string
			oldestTime time.Time
		)
		for id, seenAt := range d.seen {
			if oldestID == "" || seenAt.Before(oldestTime) {
				oldestID = id
				oldestTime = seenAt
			}
		}
		delete(d.seen, oldestID)
	}
}

package consumer

import (
	"testing"
	"time"
)

func TestDedupeTTLAndCap(t *testing.T) {
	dedupe := NewDedupe(20*time.Millisecond, 2)
	if dedupe.Seen("event-1") {
		t.Fatal("new event reported as seen")
	}
	dedupe.Add("event-1")
	if !dedupe.Seen("event-1") {
		t.Fatal("added event not seen")
	}

	time.Sleep(30 * time.Millisecond)
	if dedupe.Seen("event-1") {
		t.Fatal("expired event still seen")
	}

	dedupe.Add("event-2")
	dedupe.Add("event-3")
	dedupe.Add("event-4")
	if got := dedupe.Len(); got != 2 {
		t.Fatalf("dedupe len = %d, want cap 2", got)
	}
}

package health

import (
	"sync"
	"time"
)

// HistorySize is 24 hours of one sample a minute.
const HistorySize = 24 * 60

// Ring keeps the latest samples, oldest dropped first. It lives in memory
// only: a restart starts it again.
type Ring struct {
	mu      sync.Mutex
	samples []Sample
	next    int
	full    bool
}

// NewRing holds up to size samples.
func NewRing(size int) *Ring {
	return &Ring{samples: make([]Sample, size)}
}

// Add appends a sample, dropping the oldest when full.
func (r *Ring) Add(sample Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples[r.next] = sample
	r.next = (r.next + 1) % len(r.samples)
	if r.next == 0 {
		r.full = true
	}
}

// All is every sample, oldest first.
func (r *Ring) All() []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]Sample(nil), r.samples[:r.next]...)
	}
	out := make([]Sample, 0, len(r.samples))
	out = append(out, r.samples[r.next:]...)
	return append(out, r.samples[:r.next]...)
}

// Since is the samples taken at or after from, oldest first.
func (r *Ring) Since(from time.Time) []Sample {
	all := r.All()
	for i, sample := range all {
		if !sample.At.Before(from) {
			return all[i:]
		}
	}
	return nil
}

// Last is the latest n samples (fewer when there are fewer), oldest first.
func (r *Ring) Last(n int) []Sample {
	all := r.All()
	if len(all) > n {
		return all[len(all)-n:]
	}
	return all
}

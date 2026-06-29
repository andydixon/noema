package api

import (
	"sync"
	"time"
)

// ipBucket is a small token bucket keyed by string, used to bound webhook
// deliveries per integration (T18). Auth has its own limiter.
type ipBucket struct {
	mu      sync.Mutex
	buckets map[string]*tokens
	rate    float64
	burst   float64
}

type tokens struct {
	n    float64
	last time.Time
}

func newIPBucket(perMinute, burst int) *ipBucket {
	return &ipBucket{buckets: map[string]*tokens{}, rate: float64(perMinute) / 60, burst: float64(burst)}
}

// Allow consumes a token for key.
func (b *ipBucket) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	t := b.buckets[key]
	if t == nil {
		t = &tokens{n: b.burst, last: now}
		b.buckets[key] = t
	}
	t.n += now.Sub(t.last).Seconds() * b.rate
	if t.n > b.burst {
		t.n = b.burst
	}
	t.last = now
	if len(b.buckets) > 10000 {
		for k, v := range b.buckets {
			if now.Sub(v.last) > time.Hour {
				delete(b.buckets, k)
			}
		}
	}
	if t.n < 1 {
		return false
	}
	t.n--
	return true
}

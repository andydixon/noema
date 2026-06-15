package auth

import (
	"sync"
	"time"
)

// ipLimiter is a token bucket per client address for login attempts. It lives
// in memory because it only needs to slow down a single daemon's attackers;
// per-account lockout is persisted in the database.
type ipLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(perMinute, burst int) *ipLimiter {
	return &ipLimiter{buckets: map[string]*bucket{}, rate: float64(perMinute) / 60, burst: float64(burst), now: time.Now}
}

// Allow consumes a token for key if one is available.
func (l *ipLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Sweep drops buckets that have refilled, bounding memory (T18).
func (l *ipLimiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// lockoutDuration grows exponentially with consecutive failures, capped at
// one hour, and starts after five failures so typos are not punished.
func lockoutDuration(failed int) time.Duration {
	if failed < 5 {
		return 0
	}
	if failed > 12 { // 2^7 * 30s already exceeds the cap; avoid shift overflow
		return time.Hour
	}
	d := time.Duration(1<<uint(failed-5)) * 30 * time.Second
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

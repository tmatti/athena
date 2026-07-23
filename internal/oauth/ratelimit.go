package oauth

import (
	"sync"
	"time"
)

// limiter is a token bucket. It is global rather than per-IP: this is a
// single-user server, often behind a proxy where the peer address is not the
// caller, and the goal is bounding total attempt volume, not fairness. An
// attacker can therefore exhaust the bucket and briefly lock the owner out —
// an accepted trade-off versus letting key guesses run unthrottled.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	rate   float64 // tokens added per second
	last   time.Time
	now    func() time.Time // injectable for tests
}

func newLimiter(burst, perMinute float64) *limiter {
	return &limiter{tokens: burst, burst: burst, rate: perMinute / 60, now: time.Now}
}

// allow consumes one token, reporting whether the caller may proceed.
func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !l.last.IsZero() {
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

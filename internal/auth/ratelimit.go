package auth

import (
	"sync"
	"time"
)

// RateLimiter is a per-key fixed-window limiter used to throttle failed logins
// per client IP (ADR 0008).
type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*windowState
	now     func() time.Time
}

type windowState struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter builds a limiter allowing limit events per window per key.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*windowState),
		now:     time.Now,
	}
}

// Allow records an event for key and reports whether it is within the limit.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	st, ok := r.buckets[key]
	if !ok || now.After(st.resetAt) {
		r.buckets[key] = &windowState{count: 1, resetAt: now.Add(r.window)}
		return true
	}
	if st.count >= r.limit {
		return false
	}
	st.count++
	return true
}

package auth

import (
	"sync"
	"time"
)

// RateLimiter throttles login attempts.
//
// Without this, argon2id is doing all the work alone. It makes each guess
// expensive, but an attacker with a word list and a fast connection can still
// run through the obvious passwords given enough attempts. Rate limiting caps
// how many attempts exist to begin with, which is the cheaper half of the
// defense.
//
// The counter is per address and in memory. A shared store would be needed for
// several instances behind a load balancer, and Broadside is one process
// serving one blog, so that would be machinery for a deployment this product
// does not have.
//
// This also protects the server from itself. Each attempt costs 64MB of memory
// and roughly a tenth of a second of CPU, so a few hundred concurrent attempts
// would exhaust a small VPS. The limiter is as much about not being knocked
// over as about not being broken into.
type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*window

	// limit is how many failures are allowed inside one window.
	limit int

	// window is how long the count is remembered.
	window time.Duration
}

// window tracks failures from one address.
type window struct {
	count int
	start time.Time
}

// NewRateLimiter creates a limiter allowing limit failures per window.
func NewRateLimiter(limit int, per time.Duration) *RateLimiter {
	return &RateLimiter{
		attempts: make(map[string]*window),
		limit:    limit,
		window:   per,
	}
}

// DefaultRateLimiter is the policy applied to the login endpoint.
//
// Ten attempts in fifteen minutes. That is generous for somebody who genuinely
// mistyped their password twice and restrictive enough that a word list is
// useless: at this rate, reaching a thousand guesses takes over a day.
func DefaultRateLimiter() *RateLimiter {
	return NewRateLimiter(10, 15*time.Minute)
}

// Allow reports whether an attempt from this address may proceed.
//
// This does not itself count the attempt. A successful login should not consume
// budget, so the caller records only failures, via Fail.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, found := r.attempts[key]
	if !found {
		return true
	}

	// A fixed window rather than a sliding one. Sliding is more precise at the
	// boundary, and the precision buys nothing here: the worst case is an
	// attacker getting a double allowance across a window edge, which at ten
	// attempts per fifteen minutes is not a meaningful advantage.
	if time.Since(entry.start) > r.window {
		delete(r.attempts, key)
		return true
	}

	return entry.count < r.limit
}

// Fail records a failed attempt.
func (r *RateLimiter) Fail(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, found := r.attempts[key]
	if !found || time.Since(entry.start) > r.window {
		r.attempts[key] = &window{count: 1, start: time.Now()}
		return
	}
	entry.count++

	// Sweeping here rather than on a timer keeps the map bounded without a
	// background goroutine. Failures are the only thing that grows it, so they
	// are the right place to clean it.
	r.pruneLocked()
}

// Reset clears the record for an address, called after a successful login so
// that earlier typos do not count against the next session.
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	delete(r.attempts, key)
	r.mu.Unlock()
}

// RetryAfter reports how long until an address may try again.
func (r *RateLimiter) RetryAfter(key string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, found := r.attempts[key]
	if !found {
		return 0
	}

	remaining := r.window - time.Since(entry.start)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// pruneLocked removes expired windows. The caller holds the lock.
func (r *RateLimiter) pruneLocked() {
	// A cheap bound. Sweeping the whole map on every failure would be wasteful
	// once it is large, and it only gets large under an attack, which is
	// exactly when the server should not be doing extra work.
	if len(r.attempts) < 1000 {
		return
	}

	now := time.Now()
	for key, entry := range r.attempts {
		if now.Sub(entry.start) > r.window {
			delete(r.attempts, key)
		}
	}
}

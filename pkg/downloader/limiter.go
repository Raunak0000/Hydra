package downloader

import (
	"context"
	"sync"
	"time"
)

// RateLimiter controls byte consumption across concurrent threads.
type RateLimiter struct {
	mu           sync.Mutex
	bytesPerSec  int64
	tokens       float64
	lastCheck    time.Time
	maxBurstSecs float64
}

func NewRateLimiter(bytesPerSec int64) *RateLimiter {
	return &RateLimiter{
		bytesPerSec:  bytesPerSec,
		tokens:       float64(bytesPerSec),
		lastCheck:    time.Now(),
		maxBurstSecs: 1.0,
	}
}

func (rl *RateLimiter) SetLimit(bytesPerSec int64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.bytesPerSec = bytesPerSec
	if bytesPerSec <= 0 {
		rl.tokens = 0
		return
	}
	maxTokens := float64(bytesPerSec) * rl.maxBurstSecs
	if rl.tokens > maxTokens {
		rl.tokens = maxTokens
	}
}

// WaitN pauses execution until n bytes are granted or context cancels.
func (rl *RateLimiter) WaitN(ctx context.Context, n int) error {
	if rl == nil {
		return nil
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rl.mu.Lock()
		if rl.bytesPerSec <= 0 {
			rl.mu.Unlock()
			return nil // Speed limiting disabled
		}

		now := time.Now()
		elapsed := now.Sub(rl.lastCheck).Seconds()
		rl.lastCheck = now

		rl.tokens += elapsed * float64(rl.bytesPerSec)
		maxTokens := float64(rl.bytesPerSec) * rl.maxBurstSecs
		if rl.tokens > maxTokens {
			rl.tokens = maxTokens
		}

		needed := float64(n)
		if rl.tokens >= needed {
			rl.tokens -= needed
			rl.mu.Unlock()
			return nil
		}

		// Calculate sleep time needed to regenerate tokens
		missingTokens := needed - rl.tokens
		sleepSecs := missingTokens / float64(rl.bytesPerSec)
		rl.mu.Unlock()

		sleepDuration := time.Duration(sleepSecs * float64(time.Second))
		if sleepDuration > 200*time.Millisecond {
			sleepDuration = 200 * time.Millisecond
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepDuration):
		}
	}
}

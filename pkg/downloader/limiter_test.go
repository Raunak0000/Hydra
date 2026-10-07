package downloader

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiterAllowsReadsLargerThanBurst(t *testing.T) {
	limiter := NewRateLimiter(1024)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := limiter.WaitN(ctx, 2048); err != nil {
		t.Fatalf("WaitN failed for a read larger than the burst: %v", err)
	}
}

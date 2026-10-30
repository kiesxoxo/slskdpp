package transfers

import (
	"context"
	"sync"
	"time"
)

// bucket is a token bucket limiting bytes per second. A nil bucket or a
// zero rate does not limit.
type bucket struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(bytesPerSecond int64) *bucket {
	if bytesPerSecond <= 0 {
		return nil
	}
	r := float64(bytesPerSecond)
	burst := max(r/4, 16<<10)
	return &bucket{rate: r, burst: burst, tokens: burst, last: time.Now()}
}

// take waits until at least some of want bytes may be transferred and
// returns how many (between 1 and want).
func (b *bucket) take(ctx context.Context, want int) (int, error) {
	if b == nil || want <= 0 {
		return want, nil
	}
	for {
		b.mu.Lock()
		now := time.Now()
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
		if b.tokens >= 1 {
			n := min(want, int(b.tokens))
			b.tokens -= float64(n)
			b.mu.Unlock()
			return n, nil
		}
		need := min(float64(want), b.burst) - b.tokens
		wait := time.Duration(need / b.rate * float64(time.Second))
		b.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return 0, ctx.Err()
		case <-t.C:
		}
	}
}

package attention

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Processor interface {
	Process(context.Context, string) error
}
type TenantLister func(context.Context) ([]string, error)

// Run owns a fixed number of goroutines. The unbuffered channel applies
// backpressure to enumeration; there is never a goroutine per tenant or event.
// Cancellation reaches every query; Run joins workers before returning.
func Run(ctx context.Context, p Processor, tenants TenantLister, concurrency int, interval time.Duration, logger *slog.Logger) {
	if concurrency < 1 {
		concurrency = 4
	}
	if concurrency > 32 {
		concurrency = 32
	}
	if interval <= 0 {
		interval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for n := 0; n < concurrency; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id, ok := <-jobs:
					if !ok {
						return
					}
					call, cancel := context.WithTimeout(ctx, 10*time.Second)
					err := p.Process(call, id)
					cancel()
					if err != nil && ctx.Err() == nil {
						logger.Error("attention projection failed; will retry", "tenant", id, "err", err)
					}
				}
			}
		}()
	}
	defer func() { close(jobs); wg.Wait() }()
	for {
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		ids, err := tenants(call)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("attention tenant enumeration failed", "err", err)
		}
		for _, id := range ids {
			select {
			case <-ctx.Done():
				return
			case jobs <- id:
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

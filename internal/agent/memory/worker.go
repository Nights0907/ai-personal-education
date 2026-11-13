package memory

import (
	"context"
	"time"
)

// Run retries durable jobs in the background. Failures do not affect the chat
// request or replace the previous valid summary. Cancellation releases leases.
func (m Manager) Run(ctx context.Context, worker string, report func(error)) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := m.DispatchOne(ctx); err != nil && report != nil {
				report(err)
			}
			if _, err := m.ProcessOne(ctx, worker); err != nil && report != nil {
				report(err)
			}
		}
	}
}

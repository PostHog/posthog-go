package posthog

import "context"

func (c *client) Flush() error {
	return c.FlushWithContext(context.Background())
}

func (c *client) FlushWithContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.closed.Load() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reply := make(chan []<-chan struct{}, 1)
	select {
	case c.flushRequests <- reply:
	case <-c.quit:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	var pending []<-chan struct{}
	select {
	case pending = <-reply:
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// delivery tracks the current attempt. All fields are guarded by deliveryMu.
// Backoff ends a flush cycle, but the transport keeps ownership of the batch.
type delivery struct {
	done     chan struct{}
	retrying bool
}

func (c *client) beginDeliveryAttempt(d *delivery) {
	if d == nil {
		return
	}
	c.deliveryMu.Lock()
	if d.retrying {
		d.done = make(chan struct{})
		d.retrying = false
	}
	c.deliveryMu.Unlock()
}

func (c *client) deferDeliveryRetry(d *delivery) {
	if d == nil {
		return
	}
	c.deliveryMu.Lock()
	if !d.retrying {
		d.retrying = true
		close(d.done)
	}
	c.deliveryMu.Unlock()
}

func (c *client) completeDelivery(d *delivery) {
	c.deliveryMu.Lock()
	delete(c.deliveries, d)
	if !d.retrying {
		close(d.done)
	}
	c.deliveryMu.Unlock()
}

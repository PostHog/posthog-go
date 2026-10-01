package posthog

import "context"

func (c *client) Flush() error {
	return c.FlushWithContext(context.Background())
}

// FlushWithContext covers every started lane. Each lane's loop handles its own
// barrier, so lanes flush concurrently and one slow lane does not delay the
// other's drain.
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
	lanes := []*lane{c.analytics}
	if ai := c.ai.Load(); ai != nil {
		lanes = append(lanes, ai)
	}
	replies := make([]chan []<-chan struct{}, 0, len(lanes))
	for _, l := range lanes {
		reply := make(chan []<-chan struct{}, 1)
		select {
		case l.flushRequests <- reply:
		case <-c.quit:
			return ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
		replies = append(replies, reply)
	}
	var pending []<-chan struct{}
	for _, reply := range replies {
		select {
		case p := <-reply:
			pending = append(pending, p...)
		case <-ctx.Done():
			return ctx.Err()
		}
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

// delivery tracks the current attempt. All fields are guarded by the owning
// lane's deliveryMu. Backoff ends a flush cycle, but the transport keeps
// ownership of the batch.
type delivery struct {
	done     chan struct{}
	retrying bool
}

func (l *lane) trackDelivery() *delivery {
	d := &delivery{done: make(chan struct{})}
	l.deliveryMu.Lock()
	l.deliveries[d] = struct{}{}
	l.deliveryMu.Unlock()
	return d
}

func (l *lane) beginDeliveryAttempt(d *delivery) {
	if d == nil {
		return
	}
	l.deliveryMu.Lock()
	if d.retrying {
		d.done = make(chan struct{})
		d.retrying = false
	}
	l.deliveryMu.Unlock()
}

func (l *lane) deferDeliveryRetry(d *delivery) {
	if d == nil {
		return
	}
	l.deliveryMu.Lock()
	if !d.retrying {
		d.retrying = true
		close(d.done)
	}
	l.deliveryMu.Unlock()
}

func (l *lane) completeDelivery(d *delivery) {
	l.deliveryMu.Lock()
	delete(l.deliveries, d)
	if !d.retrying {
		close(d.done)
	}
	l.deliveryMu.Unlock()
}

// pendingDeliveries snapshots the current attempt of every in-flight batch.
func (l *lane) pendingDeliveries() []<-chan struct{} {
	l.deliveryMu.Lock()
	defer l.deliveryMu.Unlock()
	pending := make([]<-chan struct{}, 0, len(l.deliveries))
	for d := range l.deliveries {
		pending = append(pending, d.done)
	}
	return pending
}

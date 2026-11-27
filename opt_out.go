package posthog

// OptOutController is implemented by clients that can stop and resume capture
// at runtime. Assert a Client to it:
//
//	if o, ok := client.(posthog.OptOutController); ok {
//		o.OptOutCapturing()
//	}
//
// The state lives in memory for the life of the client: a server SDK holds no
// per-user consent state, so the toggle covers everything the process captures
// through this client, not one end user.
type OptOutController interface {
	// OptOutCapturing stops the client from accepting new events. Enqueue,
	// EnqueueWithContext, EnqueueAI and EnqueueAIWithContext return nil
	// without queueing anything while opted out. Events accepted before the
	// call stay queued and are still delivered, so Flush and Close behave as
	// they always have.
	OptOutCapturing()

	// OptInCapturing lets the client accept events again. Clients start
	// opted in, so this is only needed after OptOutCapturing.
	OptInCapturing()

	// IsOptedOut reports whether capture is currently turned off. A client
	// constructed without a project API key is always opted out: it sends
	// nothing.
	IsOptedOut() bool
}

var (
	_ OptOutController = (*client)(nil)
	_ OptOutController = (*noopClient)(nil)
)

// OptOutCapturing stops the client from accepting new events. See the
// OptOutCapturing method on OptOutController for the contract.
func (c *client) OptOutCapturing() {
	c.optedOut.Store(true)
}

// OptInCapturing lets the client accept events again. See the OptInCapturing
// method on OptOutController for the contract.
func (c *client) OptInCapturing() {
	c.optedOut.Store(false)
}

// IsOptedOut reports whether capture is currently turned off.
func (c *client) IsOptedOut() bool {
	return c.optedOut.Load()
}

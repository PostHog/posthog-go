package posthogmcpsdk

import (
	"context"
	"errors"
	"log"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// errNilAnalytics is reported once when the middleware is built without analytics.
var errNilAnalytics = errors.New("posthogmcpsdk: analytics is nil; tool calls are not recorded")

// disabledMiddleware reports errNilAnalytics once and returns middleware that
// passes everything through, so a missing analytics client never stops the
// server.
func disabledMiddleware(opts []Option) Middleware {
	cfg := defaultConfig(nil)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	if cfg.errorHandler != nil {
		cfg.report(context.Background(), errNilAnalytics)
	} else {
		log.Printf("[PostHog] %v", errNilAnalytics)
	}
	return Middleware{Receiving: passThrough, Sending: passThrough}
}

func passThrough(next mcpsdk.MethodHandler) mcpsdk.MethodHandler { return next }

package posthogotel

import (
	"log"
	"net/url"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// posthogAIGatewayHosts are the deployed PostHog AI Gateway hosts. The gateway
// captures its own $ai_generation on every routed call, so a service that both
// routes through it and exports spans through this bridge double-counts (and,
// for billable products, double-bills) every generation. Keep in sync with the
// sibling PostHog SDKs.
var posthogAIGatewayHosts = map[string]struct{}{
	"gateway.posthog.com":       {},
	"gateway.us.posthog.com":    {},
	"gateway.eu.posthog.com":    {},
	"ai-gateway.us.posthog.com": {},
	"ai-gateway.eu.posthog.com": {},
}

// gatewayURLAttributes are the span attribute keys whose host identifies the
// PostHog AI Gateway. They follow the GenAI/HTTP semantic conventions:
// server.address is a bare host and url.full a full URL.
var gatewayURLAttributes = [...]string{"server.address", "url.full"}

// gatewayDocsURL points at the AI observability docs referenced by the warning.
const gatewayDocsURL = "https://posthog.com/docs/ai-observability"

// schemeRE matches a leading URL scheme, so a bare host without one can be
// tolerated (for example "gateway.us.posthog.com/v1").
var schemeRE = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

// isPostHogAIGatewayURL reports whether baseURL points at a known PostHog AI
// Gateway host.
func isPostHogAIGatewayURL(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	raw := baseURL
	if !schemeRE.MatchString(raw) {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	_, ok := posthogAIGatewayHosts[host]
	return ok
}

// warnIfPostHogAIGateway logs a warning when a span's host/URL attributes point
// at the PostHog AI Gateway, which captures its own $ai_generation. It warns on
// every gateway span by design: the misconfiguration is easy to miss and a
// doubled bill is worse than a noisy log. It never drops the span, because the
// span carries data the gateway never sees.
func warnIfPostHogAIGateway(span sdktrace.ReadOnlySpan) {
	for _, attr := range span.Attributes() {
		if !isGatewayURLAttribute(string(attr.Key)) {
			continue
		}
		if attr.Value.Type() != attribute.STRING || !isPostHogAIGatewayURL(attr.Value.AsString()) {
			continue
		}
		log.Printf("[PostHog] This OpenTelemetry bridge is exporting spans from a call routed "+
			"through the PostHog AI Gateway, which captures its own $ai_generation. Every such call "+
			"is double-counted and double-billed. Use one or the other — see %s.", gatewayDocsURL)
		return
	}
}

// isGatewayURLAttribute reports whether key is one of the gateway URL attributes.
func isGatewayURLAttribute(key string) bool {
	for _, k := range gatewayURLAttributes {
		if key == k {
			return true
		}
	}
	return false
}

// Command example sends AI spans to PostHog AI observability through the
// posthogotel OpenTelemetry bridge.
//
// Google's Agent Development Kit for Go (google.golang.org/adk) instruments
// its agents with OpenTelemetry and emits gen_ai.* spans on the OpenTelemetry
// global tracer provider. Register the PostHog span processor on that provider
// before you run the agent, and the agent's gen_ai.* spans reach PostHog with
// no further code. This example emits one synthetic gen_ai.* generation with
// representative model, message, token, and response attributes in place of a
// live agent so that it runs without model credentials and is easy to inspect
// in the PostHog UI.
//
// Run it with:
//
//	POSTHOG_PROJECT_API_KEY=phc_xxx go run .
//
// Set POSTHOG_ENDPOINT to target a host other than PostHog US cloud, for
// example https://eu.i.posthog.com.
package main

import (
	"context"
	"log"
	"os"
	"time"

	posthogotel "github.com/posthog/posthog-go/otel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func main() {
	apiKey := os.Getenv("POSTHOG_PROJECT_API_KEY")
	if apiKey == "" {
		log.Fatal("set POSTHOG_PROJECT_API_KEY to your PostHog project API key")
	}

	ctx := context.Background()

	var opts []posthogotel.Option
	if host := os.Getenv("POSTHOG_ENDPOINT"); host != "" {
		opts = append(opts, posthogotel.WithHost(host))
	}

	processor, err := posthogotel.NewSpanProcessor(ctx, apiKey, opts...)
	if err != nil {
		log.Fatalf("create PostHog span processor: %v", err)
	}

	// Register the processor on the global tracer provider that ADK Go uses.
	// The resource attributes make the synthetic example easy to identify in
	// PostHog without affecting how real instrumented applications are wired.
	resource := sdkresource.NewWithAttributes("",
		attribute.String("service.name", "posthog-go-otel-example"),
		attribute.String("posthog.distinct_id", "posthog-go-otel-example"),
	)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource),
		sdktrace.WithSpanProcessor(processor),
	)
	otel.SetTracerProvider(provider)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown tracer provider: %v", err)
		}
	}()

	traceID, runID := runAgentTurn(ctx)

	// ForceFlush blocks until the queued span is exported and surfaces any
	// export error. Shutdown alone would not: the batch span processor returns
	// only context errors from Shutdown, so a rejected export (for example a bad
	// API key or host) never reaches its return value. Return on failure so the
	// deferred Shutdown still runs, and report success only once the export is
	// actually confirmed.
	flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := provider.ForceFlush(flushCtx); err != nil {
		log.Printf("send AI span to PostHog: %v", err)
		return
	}
	log.Printf("sent AI span to PostHog (trace_id=%s, example.run_id=%s)", traceID, runID)
}

// runAgentTurn emits one synthetic gen_ai.* generation with enough attributes
// to validate the conversation, model, provider, token, latency, and trace views.
// ADK Go emits its own gen_ai.* spans, but currently emits message content as log
// records rather than the span attributes used here.
func runAgentTurn(ctx context.Context) (traceID, runID string) {
	tracer := otel.Tracer("posthog-go/otel/example")
	_, span := tracer.Start(ctx, "chat posthog-go OTel example")

	runID = time.Now().UTC().Format("20060102T150405.000000000Z")
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.provider.name", "openai"),
		attribute.String("gen_ai.request.model", "gpt-4o-mini"),
		attribute.String("gen_ai.response.model", "gpt-4o-mini-2024-07-18"),
		attribute.String("gen_ai.response.id", "chatcmpl-posthog-go-example"),
		attribute.StringSlice("gen_ai.response.finish_reasons", []string{"stop"}),
		attribute.String("gen_ai.input.messages", `[{"role":"system","content":"Answer concisely."},{"role":"user","content":"What is PostHog?"}]`),
		attribute.String("gen_ai.output.messages", `[{"role":"assistant","content":"PostHog is an open-source product analytics platform."}]`),
		attribute.Int("gen_ai.usage.input_tokens", 18),
		attribute.Int("gen_ai.usage.output_tokens", 11),
		attribute.String("server.address", "api.openai.com"),
		attribute.String("example.run_id", runID),
	)

	// Make latency visible in the UI without calling an external model.
	time.Sleep(50 * time.Millisecond)
	span.End()
	return span.SpanContext().TraceID().String(), runID
}

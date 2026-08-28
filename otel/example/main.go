// Command example sends AI spans to PostHog AI observability through the
// posthogotel OpenTelemetry bridge.
//
// Google's Agent Development Kit for Go (google.golang.org/adk) instruments
// its agents with OpenTelemetry and emits gen_ai.* spans on the OpenTelemetry
// global tracer provider. Register the PostHog span processor on that provider
// before you run the agent, and the agent's gen_ai.* spans reach PostHog with
// no further code. This example emits one representative gen_ai.* span in place
// of a live agent so that it runs without model credentials.
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
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))
	otel.SetTracerProvider(provider)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown tracer provider: %v", err)
		}
	}()

	runAgentTurn(ctx)
	log.Println("sent AI span to PostHog")
}

// runAgentTurn emits one gen_ai.* span that mirrors what an instrumented agent
// records for a single model call. ADK Go produces spans like this on its own.
func runAgentTurn(ctx context.Context) {
	tracer := otel.Tracer("posthog-go/otel/example")
	_, span := tracer.Start(ctx, "gen_ai.chat gpt-4o")
	defer span.End()

	span.SetAttributes(
		attribute.String("gen_ai.system", "openai"),
		attribute.String("gen_ai.request.model", "gpt-4o"),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.Int("gen_ai.usage.input_tokens", 12),
		attribute.Int("gen_ai.usage.output_tokens", 8),
	)
}

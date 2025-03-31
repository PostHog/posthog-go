# PostHog Go Examples

The examples demonstrate different features of the PostHog Go client.

## Running the examples

```bash
# Set your PostHog API keys
export POSTHOG_PROJECT_API_KEY="your-project-api-key"
export POSTHOG_PERSONAL_API_KEY="your-personal-api-key"

# Run all examples
go run *.go
```

This will run:

- Feature flags example
- Capture events example
- Capture events with feature flag options example

### Prerequisites

Before running the examples, you'll need to:

1. Have a PostHog instance running (default: http://localhost:8000)
   - You can modify the endpoint in the example code if your instance is running elsewhere

2. Set up the following feature flags in your PostHog instance:
   - `multivariate-test` (a multivariate flag)
   - `simple-test` (a simple boolean flag)
   - `multivariate-simple-test` (a multivariate flag)
   - `my_secret_flag_value` (a remote config flag with string payload)
   - `my_secret_flag_json_object_value` (a remote config flag with JSON object payload)
   - `my_secret_flag_json_array_value` (a remote config flag with JSON array payload)

3. Set your PostHog API keys as environment variables:
   - `POSTHOG_PROJECT_API_KEY`: Your project API key (starts with `phc_...`)
   - `POSTHOG_PERSONAL_API_KEY`: Your personal API key (starts with `phx_...`)
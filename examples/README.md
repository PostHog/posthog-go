# PostHog Go Examples

The examples demonstrate different features of the PostHog Go client.

## Running the examples

### Option 1: Using .env file (Recommended)

```bash
# Copy the example .env file and fill in your credentials
cp .env.example .env
# Edit .env with your actual API keys

# Run all examples
go run *.go
```

### Option 2: Using environment variables

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

1. **PostHog API Keys**:
   - `POSTHOG_PROJECT_API_KEY`: Your project API key (starts with `phc_...`)
   - `POSTHOG_PERSONAL_API_KEY`: Your personal API key (starts with `phx_...`)

2. **PostHog Instance** (optional):
   - Default: `http://localhost:8000`
   - Set `POSTHOG_ENDPOINT` in your .env file or environment variables to use a different instance

3. **Feature Flags** (for the examples to work properly):
   Set up the following feature flags in your PostHog instance:
   - `multivariate-test` (a multivariate flag)
   - `simple-test` (a simple boolean flag)
   - `multivariate-simple-test` (a multivariate flag)
   - `my_secret_flag_value` (a remote config flag with string payload)
   - `my_secret_flag_json_object_value` (a remote config flag with JSON object payload)
   - `my_secret_flag_json_array_value` (a remote config flag with JSON array payload)

## Configuration

The examples will automatically load configuration from a `.env` file if it exists. This provides a convenient way to manage your PostHog credentials without exposing them in your shell history or environment.

### .env file format

```bash
# PostHog API Configuration
POSTHOG_PROJECT_API_KEY=phc_your_project_api_key_here
POSTHOG_PERSONAL_API_KEY=phx_your_personal_api_key_here
POSTHOG_ENDPOINT=https://app.posthog.com
```

**Note**: Environment variables take precedence over .env file values, so you can override specific settings when needed.
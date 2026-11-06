module github.com/posthog/posthog-go/posthogmcpgo

go 1.25.5

require (
	github.com/google/uuid v1.6.0
	github.com/mark3labs/mcp-go v0.58.0
	github.com/modelcontextprotocol/go-sdk v1.6.1
	github.com/posthog/posthog-go v1.42.0
	github.com/posthog/posthog-go/posthogmcpsdk v1.39.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/andybalholm/brotli v1.1.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/klauspost/compress v1.18.7 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/posthog/posthog-go => ../
	github.com/posthog/posthog-go/posthogmcpsdk => ../posthogmcpsdk
)

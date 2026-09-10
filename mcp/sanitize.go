// Portions are adapted from AgentCat-derived MCP analytics code in
// PostHog/posthog-js. See THIRD_PARTY_NOTICES.md.

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	postHogTokenPattern = regexp.MustCompile(`\bph[a-z]_[A-Za-z0-9_-]{20,}\b`)
	sensitiveKeyPattern = regexp.MustCompile(`(?i)^(authorization|cookie|set-cookie|x-api-key|api[-_]?key|api[-_]?token|access[-_]?token|refresh[-_]?token|token|password|secret|client[-_]?secret|private[-_]?key)$`)
	base64Pattern       = regexp.MustCompile(`^[A-Za-z0-9+/\r\n]+=*$`)
	base64URLPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]+={0,2}$`)
	base64DataPrefix    = regexp.MustCompile(`(?i)^data:[^,\s]*;base64,`)
	base64DataPayload   = regexp.MustCompile(`^[A-Za-z0-9+/_-]+={0,2}$`)
)

func normalizePayload(field string, value any) (normalized any, err error) {
	if value == nil {
		return nil, nil
	}

	data, err := marshalJSONSafely(value)
	if err != nil {
		return nil, packageError("normalize "+field, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return nil, packageError("normalize "+field, err)
	}
	return normalized, nil
}

func marshalJSONSafely(value any) (data []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			data = nil
			err = fmt.Errorf("JSON marshaler panic (%T)", recovered)
		}
	}()
	return json.Marshal(value)
}

func packageError(stage string, cause error) error {
	message := fmt.Sprintf("posthogmcp: %s: %T", stage, cause)
	return errors.New(truncateUTF8(message, maxReturnedErrorBytes))
}

func sanitizeCapturedValue(value any) any {
	switch value := value.(type) {
	case string:
		return sanitizeString(value)
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = sanitizeCapturedValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			if sensitiveKeyPattern.MatchString(key) {
				result[key] = redactedValue
			} else {
				result[key] = sanitizeCapturedValue(item)
			}
		}
		return result
	default:
		return value
	}
}

func sanitizeString(value string) string {
	if len(value) >= largeBinaryGateBytes && isBase64Like(value) {
		return binaryRedactedValue
	}
	return postHogTokenPattern.ReplaceAllString(value, redactedValue)
}

func isBase64Like(value string) bool {
	if base64Pattern.MatchString(value) {
		return true
	}
	if strings.ContainsAny(value, "-_") && base64URLPattern.MatchString(value) {
		return true
	}

	prefix := base64DataPrefix.FindString(value)
	if prefix == "" {
		return false
	}
	payload, err := url.PathUnescape(value[len(prefix):])
	if err != nil {
		return false
	}
	payload = strings.NewReplacer("\r", "", "\n", "").Replace(payload)
	return base64DataPayload.MatchString(payload)
}

func sanitizeResponse(value any) any {
	sanitized := sanitizeCapturedValue(value)
	response, ok := sanitized.(map[string]any)
	if !ok {
		return sanitized
	}

	result := cloneMap(response)
	if content, ok := result["content"].([]any); ok {
		sanitizedContent := make([]any, len(content))
		for i, block := range content {
			sanitizedContent[i] = sanitizeContentBlock(block)
		}
		result["content"] = sanitizedContent
	}
	return result
}

func sanitizeContentBlock(value any) any {
	block, ok := value.(map[string]any)
	if !ok {
		return value
	}

	contentType, _ := block["type"].(string)
	switch contentType {
	case "text", "resource_link":
		return sanitizeCapturedValue(block)
	case "image":
		return redactedContentBlock("[image content redacted - not supported by PostHog MCP analytics]")
	case "audio":
		return redactedContentBlock("[audio content redacted - not supported by PostHog MCP analytics]")
	case "resource":
		if resource, ok := block["resource"].(map[string]any); ok {
			if _, hasBlob := resource["blob"]; hasBlob {
				return redactedContentBlock("[binary resource content redacted - not supported by PostHog MCP analytics]")
			}
		}
		return sanitizeCapturedValue(block)
	default:
		return redactedContentBlock(fmt.Sprintf(
			"[unsupported content type %q redacted - not supported by PostHog MCP analytics]",
			contentType,
		))
	}
}

func redactedContentBlock(message string) map[string]any {
	return map[string]any{"type": "text", "text": message}
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

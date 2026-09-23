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
	"unicode/utf8"
)

var (
	postHogTokenPattern = regexp.MustCompile(`\bph[a-z]_[A-Za-z0-9_-]{20,}\b`)
	sensitiveKeyPattern = regexp.MustCompile(`(?i)^(authorization|cookie|set-cookie|x-api-key|api[-_]?key|api[-_]?token|access[-_]?token|refresh[-_]?token|token|password|secret|client[-_]?secret|private[-_]?key)$`)
	base64Pattern       = regexp.MustCompile(`^[A-Za-z0-9+/\r\n]+=*$`)
	base64URLPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]+={0,2}$`)
	base64DataPrefix    = regexp.MustCompile(`(?i)^data:[^,\s]*;base64,`)
	base64DataPayload   = regexp.MustCompile(`^[A-Za-z0-9+/_-]+={0,2}$`)

	// Intent-only structured identifiers. Patterns follow the Python and
	// TypeScript MCP sanitizers: bounded quantifiers, ASCII classes, and
	// horizontal Unicode spaces normalized before matching. Go's RE2 engine
	// has no lookaround, so IPv6 and phone boundaries are checked beside the
	// match instead.
	unicodeHorizontalSpacePattern = regexp.MustCompile("[\u00a0\u1680\u2000-\u200a\u202f\u205f\u3000]")
	emailPattern                  = regexp.MustCompile(`[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9.-]{1,255}\.[A-Za-z]{2,24}`)
	ipv4Pattern                   = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b`)
	ipv6FullPattern               = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}\b`)
	ipv6TailPattern               = regexp.MustCompile(`(?:[0-9A-Fa-f]{1,4}:){1,7}:`)
	ipv6MidPattern                = regexp.MustCompile(`(?:[0-9A-Fa-f]{1,4}:){1,6}:[0-9A-Fa-f]{1,4}(?::[0-9A-Fa-f]{1,4}){0,5}`)
	ipv6LeadPattern               = regexp.MustCompile(`::(?:[0-9A-Fa-f]{1,4}(?::[0-9A-Fa-f]{1,4}){0,6})`)
	creditCardCandidatePattern    = regexp.MustCompile(`\b\d(?:[ ./-]?\d){12,}\b`)
	digitGroupPattern             = regexp.MustCompile(`\d+`)
	ssnPattern                    = regexp.MustCompile(`\b\d{3}[ .-]\d{2}[ .-]\d{4}\b`)
	phoneNANPPattern              = regexp.MustCompile(`(?:\+?1[ ./-]?)?(?:\(\d{3}\)[ ./-]?|\d{3}[ ./-])\d{3}[ ./-]\d{4}`)
	phoneIntlPattern              = regexp.MustCompile(`\+\d{1,3}(?:[ ./()-]{0,2}\d){7,13}`)
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

func redactIntent(value string) string {
	result := unicodeHorizontalSpacePattern.ReplaceAllString(value, " ")
	result = emailPattern.ReplaceAllString(result, redactedValue)
	result = ipv4Pattern.ReplaceAllString(result, redactedValue)
	result = ipv6FullPattern.ReplaceAllString(result, redactedValue)
	result = replaceIf(ipv6TailPattern, result, func(value string, start, end int) bool {
		return leftAllows(value, start, ":") && rightAllows(value, end, ":")
	})
	result = replaceIf(ipv6MidPattern, result, func(value string, start, end int) bool {
		return leftAllows(value, start, ":") && rightAllows(value, end, "")
	})
	result = replaceIf(ipv6LeadPattern, result, func(value string, start, end int) bool {
		return leftAllows(value, start, ":") && rightAllows(value, end, "")
	})
	result = creditCardCandidatePattern.ReplaceAllStringFunc(result, redactCardInMatch)
	result = ssnPattern.ReplaceAllString(result, redactedValue)
	result = replaceIf(phoneNANPPattern, result, func(value string, start, end int) bool {
		return leftAllows(value, start, "+") && rightAllows(value, end, "")
	})
	result = replaceIf(phoneIntlPattern, result, func(value string, start, end int) bool {
		return leftAllows(value, start, "") && rightAllows(value, end, "")
	})
	return result
}

func replaceIf(pattern *regexp.Regexp, value string, accept func(value string, start, end int) bool) string {
	locs := pattern.FindAllStringIndex(value, -1)
	if len(locs) == 0 {
		return value
	}

	var b strings.Builder
	last := 0
	replaced := false
	for _, loc := range locs {
		if loc[0] < last || !accept(value, loc[0], loc[1]) {
			continue
		}
		b.WriteString(value[last:loc[0]])
		b.WriteString(redactedValue)
		last = loc[1]
		replaced = true
	}
	if !replaced {
		return value
	}
	b.WriteString(value[last:])
	return b.String()
}

func leftAllows(value string, index int, extraForbidden string) bool {
	if index == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(value[:index])
	return !isASCIIWord(r) && !strings.ContainsRune(extraForbidden, r)
}

func rightAllows(value string, index int, extraForbidden string) bool {
	if index == len(value) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(value[index:])
	return !isASCIIWord(r) && !strings.ContainsRune(extraForbidden, r)
}

func isASCIIWord(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_'
}

func redactCardInMatch(text string) string {
	groups := digitGroupPattern.FindAllStringIndex(text, -1)
	if len(groups) == 0 {
		return text
	}

	var b strings.Builder
	cursor := 0
	for first := 0; first < len(groups); {
		digits := ""
		matchedLast := -1
		for last := first; last < len(groups); last++ {
			digits += text[groups[last][0]:groups[last][1]]
			if len(digits) > 19 {
				break
			}
			if len(digits) >= 13 && passesLuhn(digits) {
				matchedLast = last
			}
		}
		if matchedLast >= 0 {
			b.WriteString(text[cursor:groups[first][0]])
			b.WriteString(redactedValue)
			cursor = groups[matchedLast][1]
			first = matchedLast + 1
			continue
		}
		first++
	}
	b.WriteString(text[cursor:])
	return b.String()
}

func passesLuhn(digits string) bool {
	total := 0
	double := false
	for index := len(digits) - 1; index >= 0; index-- {
		digit := int(digits[index] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		total += digit
		double = !double
	}
	return total%10 == 0
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

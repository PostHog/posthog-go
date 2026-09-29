// Portions are adapted from AgentCat-derived MCP analytics code in
// PostHog/posthog-js. See THIRD_PARTY_NOTICES.md.

package posthogmcp

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
	sensitiveKeyPattern = regexp.MustCompile(`(?i)^(authorization|cookie|set-cookie|x-api-key|api[-_]?key|api[-_]?token|access[-_]?token|refresh[-_]?token|token|password|secret|client[-_]?secret|private[-_]?key)\n?$`)
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
	creditCardCandidatePattern    = regexp.MustCompile(`\b\d(?:[ ./-]?\d){12,}\b`)
	digitGroupPattern             = regexp.MustCompile(`\d+`)
	ssnPattern                    = regexp.MustCompile(`\b\d{3}[ .-]\d{2}[ .-]\d{4}\b`)

	// One pattern with four alternatives, as in Python: a single pass, so each
	// alternative's lookbehind sees the original text rather than an earlier
	// alternative's redaction.
	ipv6Pattern = lookaroundPattern{
		lookaround("", `(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}`, ""),
		lookaround(":", `(?:[0-9A-Fa-f]{1,4}:){1,7}:`, ":"),
		lookaround(":", `(?:[0-9A-Fa-f]{1,4}:){1,6}:[0-9A-Fa-f]{1,4}(?::[0-9A-Fa-f]{1,4}){0,5}`, ""),
		lookaround(":", `::(?:[0-9A-Fa-f]{1,4}(?::[0-9A-Fa-f]{1,4}){0,6})`, ""),
	}
	phoneNANPPattern = lookaroundPattern{
		lookaround("+", `(?:\+?1[ ./-]?)?(?:\(\d{3}\)[ ./-]?|\d{3}[ ./-])\d{3}[ ./-]\d{4}`, ""),
	}
	phoneIntlPattern = lookaroundPattern{
		lookaround("", `\+\d{1,3}(?:[ ./()-]{0,2}\d){7,13}`, ""),
	}
)

func normalizePayload(field string, value any) (normalized any, err error) {
	if value == nil {
		return nil, nil
	}

	data, err := marshalJSONSafely(value)
	if err != nil {
		return nil, packageError("normalize "+field, err)
	}
	if len(data) > maxNormalizeBytes {
		if field == "Parameters" || field == "Response" {
			return oversizedPayloadValue, nil
		}
		return nil, fmt.Errorf("posthogmcp: %s exceeds MCP analytics input limit", field)
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
	result = ipv6Pattern.redact(result)
	result = creditCardCandidatePattern.ReplaceAllStringFunc(result, redactCardInMatch)
	result = ssnPattern.ReplaceAllString(result, redactedValue)
	result = phoneNANPPattern.redact(result)
	result = phoneIntlPattern.redact(result)
	return result
}

// A lookaroundPattern is a Python regex whose alternatives are guarded by
// lookbehind and lookahead, which RE2 lacks. Each alternative is anchored at
// every position its lookbehind allows, with the lookahead consumed as a
// trailing class, so RE2's leftmost-first submatch picks the match Python's
// backtracking would: `2001:db8::1:8080x` gives up `:8080` to match.
type lookaroundPattern []lookaroundAlternative

type lookaroundAlternative struct {
	notAfter string
	anchored *regexp.Regexp
}

// lookaround builds an alternative that may not follow an ASCII word
// character or one of notAfter, nor precede an ASCII word character or one of
// notBefore.
func lookaround(notAfter, body, notBefore string) lookaroundAlternative {
	return lookaroundAlternative{
		notAfter: notAfter,
		anchored: regexp.MustCompile(`^(` + body + `)(?:[^\w` + notBefore + `]|$)`),
	}
}

func (pattern lookaroundPattern) redact(value string) string {
	var b strings.Builder
	last := 0
	for start := 0; start < len(value); start++ {
		end := pattern.matchEnd(value, start)
		if end < 0 {
			continue
		}
		b.WriteString(value[last:start])
		b.WriteString(redactedValue)
		last = end
		start = end - 1
	}
	if last == 0 {
		return value
	}
	b.WriteString(value[last:])
	return b.String()
}

func (pattern lookaroundPattern) matchEnd(value string, start int) int {
	before, _ := utf8.DecodeLastRuneInString(value[:start])
	for _, alternative := range pattern {
		if start > 0 && (isASCIIWord(before) || strings.ContainsRune(alternative.notAfter, before)) {
			continue
		}
		if loc := alternative.anchored.FindStringSubmatchIndex(value[start:]); loc != nil {
			return start + loc[3]
		}
	}
	return -1
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
	if isBinaryBlob(value) {
		return binaryRedactedValue
	}
	return sanitizeURLs(redactCredentials(value), true)
}

func isBinaryBlob(value string) bool {
	return len(value) >= largeBinaryGateBytes && isBase64Like(value)
}

// redactCredentials runs before the URL pass, because rewriting a URL changes
// the text these detectors match: it percent-encodes the `/` in front of a
// `?ref=/phx_...` token, and can grow a word past the known-format scan window.
func redactCredentials(value string) string {
	return redactSecretTokens(postHogTokenPattern.ReplaceAllString(value, redactedValue))
}

// redactSecretTokens redacts each space-separated word that reads as a
// credential, so an error message keeps its diagnostic prose.
func redactSecretTokens(value string) string {
	words := strings.Split(value, " ")
	changed := false
	for i, word := range words {
		if isSecretWord(word) {
			words[i] = redactedValue
			changed = true
		}
	}
	if !changed {
		return value
	}
	return strings.Join(words, " ")
}

// isSecretWord judges a word without this sanitizer's own markers: a value
// can be sanitized twice, and the marker's character mix alone can push a
// short uri like `resource:guide?token=%5Bredacted%5D` over the entropy bar.
func isSecretWord(word string) bool {
	word = strings.ReplaceAll(word, encodedRedactedValue, "")
	word = strings.ReplaceAll(word, redactedValue, "")
	return word != "" && looksLikeSecret(word)
}

// sanitizeFreeText sanitizes agent-narrated text such as the intent, in an
// order where each step protects the next. The binary gate runs first, because
// a redaction spliced into a base64 blob stops it looking like base64.
// Credentials run before structured PII, which reads the middle of
// `phx_AAAA-415-555-0142-AAAA` as a phone number and would leave the token's
// halves behind. PII runs before URLs, because a rewritten URL percent-encodes
// the `@` the email pattern needs.
func sanitizeFreeText(value string) string {
	if isBinaryBlob(value) {
		return binaryRedactedValue
	}
	return sanitizeURLs(redactIntent(redactCredentials(value)), true)
}

// sanitizeResourceName sanitizes a tool name or resource uri without the
// entropy detector, which reads a name like `Get_Organization_Memberships`
// as a credential and would cost every per-tool metric.
func sanitizeResourceName(value string) string {
	return sanitizeURLs(postHogTokenPattern.ReplaceAllString(value, redactedValue), true)
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

// Redact media in common decoded MCP responses before JSON normalization so
// large binary fields are not serialized and decoded merely to be discarded.
func redactMediaBeforeNormalize(value any) any {
	response, ok := value.(map[string]any)
	if !ok {
		return value
	}
	var content []any
	switch blocks := response["content"].(type) {
	case []any:
		content = blocks
	case []map[string]any:
		content = make([]any, len(blocks))
		for i, block := range blocks {
			content[i] = block
		}
	default:
		return value
	}

	redacted := make([]any, len(content))
	changed := false
	for i, block := range content {
		redacted[i] = block
		if object, ok := block.(map[string]any); ok {
			if replacement, ok := redactedMediaBlock(object); ok {
				redacted[i] = replacement
				changed = true
			}
		}
	}
	if !changed {
		return value
	}
	result := cloneMap(response)
	result["content"] = redacted
	return result
}

func redactedMediaBlock(block map[string]any) (map[string]any, bool) {
	switch block["type"] {
	case "image":
		return redactedContentBlock("[image content redacted - not supported by PostHog MCP analytics]"), true
	case "audio":
		return redactedContentBlock("[audio content redacted - not supported by PostHog MCP analytics]"), true
	case "resource":
		if resource, ok := block["resource"].(map[string]any); ok {
			if _, hasBlob := resource["blob"]; hasBlob {
				return redactedContentBlock("[binary resource content redacted - not supported by PostHog MCP analytics]"), true
			}
		}
	}
	return nil, false
}

func sanitizeContentBlock(value any) any {
	block, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if replacement, ok := redactedMediaBlock(block); ok {
		return replacement
	}

	contentType, _ := block["type"].(string)
	switch contentType {
	case "text", "resource_link", "resource":
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

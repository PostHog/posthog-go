package posthogmcp

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// A port of posthog-python's _sanitize_urls: redact URL userinfo and the
// values of credential-bearing query and fragment fields, one level into
// nested URLs, returning any URL with nothing to redact byte-for-byte.

// Python's `\s` on str, which is wider than RE2's ASCII `\s`.
const pythonWhitespace = `\t\n\v\f\r \x1c-\x1f\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

const (
	urlSchemePattern = `(?i)[a-z][a-z0-9+.-]{0,63}`
	urlBodyPattern   = `:[^` + pythonWhitespace + `<>"]+`

	// Split off a URL found in prose and re-appended, so the sentence keeps
	// its punctuation. `'` is in the URL body but closes a quote at its end.
	urlTrailingPunctuation = ".,;:!?)]}'"

	encodedRedactedValue = "%5Bredacted%5D"
	maxURLLength         = 8192
	maxURLQueryFields    = 128
)

var (
	// No leading `\b`, so `resource_https://user:pw@host` still matches. The
	// authority is optional because an MCP resource uri need not have one.
	urlPattern      = regexp.MustCompile(urlSchemePattern + urlBodyPattern)
	wholeURLPattern = regexp.MustCompile(`^` + urlSchemePattern + urlBodyPattern + `$`)

	// The length bound caps parsing work on an attacker-shaped authority, so it
	// only applies to a match that opens with one: a long data uri must pass.
	urlAuthorityPrefix = regexp.MustCompile(`^` + urlSchemePattern + `://`)
	urlAuthority       = regexp.MustCompile(urlSchemePattern + `://`)

	// A query key is sensitive when any delimited segment names a credential
	// (`private_token`, `X-Amz-Security-Token`). The `\n?$` ends mirror
	// Python's `$`, which also matches before a trailing newline.
	sensitiveQuerySegmentPattern = regexp.MustCompile(`(?i)(?:^|[-_./;])(?:auth|token|secret|password|passwd|pwd|credential|signature|sig|key|hmac|sas|bearer|jwt|session|sessionid)(?:[-_./;]|\n?$)`)
	// Matched whole: `code` as a segment would eat `country_code`.
	sensitiveQueryKeyPattern = regexp.MustCompile(`(?i)^(?:code|AWSAccessKeyId|GoogleAccessId|Policy)\n?$`)
)

func shouldRedactQueryKey(key string) bool {
	return sensitiveKeyPattern.MatchString(key) ||
		sensitiveQuerySegmentPattern.MatchString(key) ||
		sensitiveQueryKeyPattern.MatchString(key)
}

func sanitizeURLs(text string, nested bool) string {
	// A string that is one URL has no prose around it, so its tail belongs to
	// the URL: `?password=hunter2!!!` ends in the password itself.
	if wholeURLPattern.MatchString(text) {
		return sanitizeURL(text, nested, false)
	}
	return urlPattern.ReplaceAllStringFunc(text, func(match string) string {
		return sanitizeURL(match, nested, true)
	})
}

func sanitizeURL(value string, nested, inProse bool) string {
	var b strings.Builder
	for _, piece := range splitAddresses(value) {
		b.WriteString(sanitizeSingleURL(piece, nested, inProse))
	}
	return b.String()
}

// splitAddresses cuts a match into the addresses it runs together (`URL:https://...`,
// `/doc,https://...`), so a second address cannot hide in the first one's path.
// An address in a field's value stays with that field: the nested pass sanitizes
// it there, and splitting it off would publish the value's tail.
func splitAddresses(value string) []string {
	fieldsStart := len(value)
	if i := strings.IndexAny(value, "?#"); i >= 0 {
		fieldsStart = i
	}
	pieces := []string{}
	begin := 0
	for _, start := range authorityStarts(value) {
		if start.index > 0 && (start.index < fieldsStart || start.structural != '=') {
			pieces = append(pieces, value[begin:start.index])
			begin = start.index
		}
	}
	return append(pieces, value[begin:])
}

type authorityStart struct {
	index      int
	structural byte
}

// authorityStarts pairs every authority start with the last structural
// character before it, in one forward pass.
func authorityStarts(value string) []authorityStart {
	query, fragment, fragmentTail := structuralDelimiters(value)
	var starts []authorityStart
	cursor := 0
	var structural byte
	for _, loc := range urlAuthority.FindAllStringIndex(value, -1) {
		for i := cursor; i < loc[0]; i++ {
			// The fragment's own `?` divides it only when no value is open.
			if value[i] == '=' || value[i] == '&' || i == query || i == fragment ||
				(i == fragmentTail && structural != '=') {
				structural = value[i]
			}
		}
		cursor = loc[0]
		starts = append(starts, authorityStart{index: loc[0], structural: structural})
	}
	return starts
}

// structuralDelimiters finds the query's `?`, the fragment's `#`, and the `?`
// splitting the fragment (-1 when absent). Any other `?` or `#` is a character
// of a value.
func structuralDelimiters(value string) (query, fragment, fragmentTail int) {
	fragment = strings.IndexByte(value, '#')
	query = strings.IndexByte(value, '?')
	if fragment != -1 && (query == -1 || query > fragment) {
		query = -1
	}
	fragmentTail = -1
	if fragment != -1 {
		if i := strings.IndexByte(value[fragment+1:], '?'); i >= 0 {
			fragmentTail = fragment + 1 + i
		}
	}
	return query, fragment, fragmentTail
}

func sanitizeSingleURL(value string, nested, inProse bool) string {
	if utf8.RuneCountInString(value) > maxURLLength && urlAuthorityPrefix.MatchString(value) {
		return redactedValue
	}
	text := value
	if inProse {
		text = strings.TrimRight(value, urlTrailingPunctuation)
	}
	suffix := value[len(text):]

	rewritten, changed, endsInCredential, err := rewriteURL(text, nested)
	switch {
	case err != nil:
		return redactedValue + suffix
	case !changed:
		return value
	case endsInCredential:
		// The split-off punctuation may be the credential's own tail.
		return rewritten
	default:
		return rewritten + suffix
	}
}

func rewriteURL(text string, nested bool) (rewritten string, changed, endsInCredential bool, err error) {
	u, err := urlsplit(text)
	if err != nil {
		return "", false, false, err
	}
	query, err := parseQSL(u.query, maxURLQueryFields)
	if err != nil {
		return "", false, false, err
	}
	sanitizedQuery := sanitizeURLFields(query, nested)

	// A `?` inside a fragment splits it into two halves judged on their own:
	// `#/callback?token=x` is text then fields, `#a=1?b=2` is fields twice.
	head, tail, hasTail := strings.Cut(u.fragment, "?")
	sanitizedHead, headRewroteLast, err := sanitizeFragmentPart(head, nested)
	if err != nil {
		return "", false, false, err
	}
	sanitizedTail, tailRewroteLast := redactedValue, true
	// A `?` can fall inside a credential (`#password=pre?fix`), so when the head
	// ends in a redacted value the tail goes too.
	if !headRewroteLast || tail == "" {
		sanitizedTail, tailRewroteLast, err = sanitizeFragmentPart(tail, nested)
		if err != nil {
			return "", false, false, err
		}
	}
	fragment := sanitizedHead
	if hasTail {
		fragment += "?" + sanitizedTail
	}
	netloc := redactUserinfo(u.netloc)

	queryChanged := !equalFields(query, sanitizedQuery)
	if netloc == u.netloc && !queryChanged && fragment == u.fragment {
		return "", false, false, nil
	}

	switch {
	case u.fragment == "":
		endsInCredential = lastFieldIsSensitive(query, sanitizedQuery)
	case strings.Contains(u.fragment, "?"):
		endsInCredential = tailRewroteLast
	default:
		endsInCredential = headRewroteLast
	}

	// Only the part that changed is re-serialized, so an untouched query keeps
	// its original encoding.
	u.netloc = netloc
	u.fragment = fragment
	if queryChanged {
		u.query = urlencode(sanitizedQuery)
	}
	return urlunsplit(u), true, endsInCredential, nil
}

// sanitizeFragmentPart runs the field pass on a fragment half holding a `=`
// and the text pass otherwise, reporting whether its last field was rewritten.
func sanitizeFragmentPart(text string, nested bool) (string, bool, error) {
	if !strings.Contains(text, "=") {
		return sanitizeFragmentText(text, nested), false, nil
	}
	fields, err := parseQSL(text, maxURLQueryFields)
	if err != nil {
		return "", false, err
	}
	sanitized := sanitizeURLFields(fields, nested)
	if !equalFields(fields, sanitized) {
		text = urlencode(sanitized)
	}
	return text, lastFieldIsSensitive(fields, sanitized), nil
}

// lastFieldIsSensitive reports whether the last field carries a credential.
// The key alone has to count: an earlier pass may already have redacted the
// value, leaving the comparison nothing to catch.
func lastFieldIsSensitive(fields, sanitized []urlField) bool {
	if len(fields) == 0 {
		return false
	}
	last := len(fields) - 1
	return shouldRedactQueryKey(fields[last].key) || sanitized[last] != fields[last]
}

// sanitizeFragmentText handles fragment text, which can carry an address no
// other pass sees. Past the one-level nesting budget such text is dropped,
// which is also what stops a `#`-chained uri from recursing without end.
func sanitizeFragmentText(text string, nested bool) string {
	if nested {
		return sanitizeURLs(text, false)
	}
	if urlPattern.MatchString(text) {
		return redactedValue
	}
	return text
}

func redactUserinfo(netloc string) string {
	at := strings.LastIndexByte(netloc, '@')
	if at < 0 {
		return netloc
	}
	return encodedRedactedValue + netloc[at:]
}

func sanitizeURLFields(fields []urlField, nested bool) []urlField {
	sanitized := make([]urlField, len(fields))
	for i, field := range fields {
		sanitized[i] = urlField{key: field.key, value: sanitizeURLFieldValue(field.key, field.value, nested)}
	}
	return sanitized
}

func sanitizeURLFieldValue(key, value string, nested bool) string {
	if shouldRedactQueryKey(key) {
		return redactedValue
	}
	// Some servers split fields on `;` and others do not. Splitting would
	// publish the tail of `password=pre;fix`, so the whole value goes when any
	// `;` piece names a credential.
	if strings.Contains(value, ";") && namesACredential(value) {
		return redactedValue
	}
	// One level of nesting is sanitized; a value still carrying a URL past it
	// is dropped rather than trusted.
	if urlPattern.MatchString(value) {
		if nested {
			return sanitizeURLs(value, false)
		}
		return redactedValue
	}
	return value
}

func namesACredential(value string) bool {
	for _, piece := range strings.Split(value, ";") {
		key, _, _ := strings.Cut(piece, "=")
		if shouldRedactQueryKey(key) {
			return true
		}
	}
	return false
}

func equalFields(a, b []urlField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

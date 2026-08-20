// Portions are adapted from AgentCat-derived MCP analytics code in
// PostHog/posthog-js. See THIRD_PARTY_NOTICES.md.

package mcp

import (
	"sort"
	"strings"
	"unicode/utf8"
)

func truncateValue(value any) any {
	return truncateNested(value, maxDepth)
}

func truncateNested(value any, remainingDepth int) any {
	switch value := value.(type) {
	case string:
		return truncateUTF8(value, maxStringBytes)
	case []any:
		if remainingDepth <= 0 {
			return "[Array]"
		}
		limit := len(value)
		truncated := false
		if limit > maxBreadth {
			limit = maxBreadth - 1
			truncated = true
		}
		result := make([]any, 0, limit+1)
		for _, item := range value[:limit] {
			result = append(result, truncateNested(item, remainingDepth-1))
		}
		if truncated {
			result = append(result, "[MaxProperties ~]")
		}
		return result
	case map[string]any:
		if remainingDepth <= 0 {
			return "[Object]"
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		limit := len(keys)
		truncated := false
		if limit > maxBreadth {
			limit = maxBreadth - 1
			truncated = true
		}
		result := make(map[string]any, limit+1)
		for _, key := range keys[:limit] {
			result[key] = truncateNested(value[key], remainingDepth-1)
		}
		if truncated {
			result["..."] = "[MaxProperties ~]"
		}
		return result
	default:
		return value
	}
}

func truncateUTF8(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= limit {
		return value
	}
	if limit <= len(truncationSuffix) {
		return truncationSuffix[:limit]
	}

	end := limit - len(truncationSuffix)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + truncationSuffix
}

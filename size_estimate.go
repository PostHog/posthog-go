package posthog

// estimateJSONSize estimates the JSON-encoded size of a value in bytes.
// This is used for batch size calculations without performing actual JSON encoding.
// The estimate may not be exact but should be close enough for batching decisions.
func estimateJSONSize(v interface{}) int {
	switch val := v.(type) {
	case string:
		// String: quotes + content + potential escaping overhead (~10%)
		return len(val) + 2 + len(val)/10
	case int, int8, int16, int32, int64:
		return 12 // typical integer
	case uint, uint8, uint16, uint32, uint64:
		return 12 // typical unsigned integer
	case float32, float64:
		return 16 // typical float with decimals
	case bool:
		return 5 // "true" or "false"
	case nil:
		return 4 // "null"
	case map[string]interface{}:
		size := 2 // {}
		for k, v := range val {
			// "key": value,
			size += len(k) + 4 + estimateJSONSize(v)
		}
		return size
	case Properties:
		size := 2 // {}
		for k, v := range val {
			size += len(k) + 4 + estimateJSONSize(v)
		}
		return size
	case Groups:
		size := 2 // {}
		for k, v := range val {
			size += len(k) + 4 + estimateJSONSize(v) // "key":value,
		}
		return size
	case []interface{}:
		size := 2 // []
		for _, item := range val {
			size += estimateJSONSize(item) + 1 // item + comma
		}
		return size
	case []string:
		size := 2 // []
		for _, s := range val {
			size += len(s) + 3 // "string",
		}
		return size
	default:
		return 20 // fallback for unknown types
	}
}

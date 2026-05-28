package truncate

import (
	"encoding/json"
)

// truncatedSuffix is appended to string values that exceed maxLen.
const truncatedSuffix = "...[truncated]"

// TruncateJSONStringLeaves parses data as JSON and truncates any string leaf
// value longer than maxLen characters by keeping the first maxLen characters
// and appending truncatedSuffix. The result is re-serialized to valid JSON.
//
// If data is not valid JSON, the function falls back to simple byte truncation:
// it returns data[:maxLen] if len(data) > maxLen, otherwise data unchanged.
//
// Handles all JSON value types: objects, arrays, strings, numbers, booleans,
// null. Only string leaves are truncated.
func TruncateJSONStringLeaves(data []byte, maxLen int) []byte {
	if maxLen <= 0 || len(data) == 0 {
		return data
	}

	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		// Not valid JSON — fall back to simple byte truncation.
		if len(data) > maxLen {
			out := make([]byte, maxLen)
			copy(out, data[:maxLen])
			return out
		}
		return data
	}

	truncated := truncateValue(v, maxLen)
	out, err := json.Marshal(truncated)
	if err != nil {
		// Serialization failure is unexpected — return original.
		return data
	}
	return out
}

// truncateValue recursively walks a JSON-decoded value and truncates string
// leaves. The input is one of: map[string]interface{}, []interface{},
// string, float64, bool, or nil.
func truncateValue(v interface{}, maxLen int) interface{} {
	switch val := v.(type) {
	case string:
		runes := []rune(val)
		if len(runes) > maxLen {
			return string(runes[:maxLen]) + truncatedSuffix
		}
		return val

	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, child := range val {
			out[k] = truncateValue(child, maxLen)
		}
		return out

	case []interface{}:
		out := make([]interface{}, len(val))
		for i, child := range val {
			out[i] = truncateValue(child, maxLen)
		}
		return out

	default:
		// float64, bool, nil — pass through unchanged.
		return v
	}
}

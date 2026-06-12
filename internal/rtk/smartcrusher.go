// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.

package rtk

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/ccr"
)

// minCrushBytes is the minimum input size before crushJSON attempts compression.
const minCrushBytes = 1024

// crushJSON attempts structural JSON compression on s.
// It returns the (possibly compressed) string and whether meaningful compression occurred.
// It never returns an empty string for a non-empty input.
func crushJSON(s string) (string, bool) {
	if len(s) < minCrushBytes {
		return s, false
	}

	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s, false
	}

	switch val := v.(type) {
	case []any:
		return crushArray(s, val)
	case map[string]any:
		return crushObject(s, val)
	}
	return s, false
}

// crushArray handles Case 1: top-level array of homogeneous objects.
func crushArray(original string, arr []any) (string, bool) {
	if len(arr) < 4 {
		return original, false
	}

	// Verify all elements are objects.
	objects := make([]map[string]any, 0, len(arr))
	for _, elem := range arr {
		obj, ok := elem.(map[string]any)
		if !ok {
			return original, false
		}
		objects = append(objects, obj)
	}

	// Require at least 1 key per object.
	if len(objects) == 0 || len(objects[0]) == 0 {
		return original, false
	}

	// Extract key names from the first object, sorted for stability.
	keys := make([]string, 0, len(objects[0]))
	for k := range objects[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	n := len(objects)

	var sb strings.Builder
	fmt.Fprintf(&sb, "[%d objects — keys: %s]\n", n, strings.Join(keys, ", "))
	fmt.Fprintf(&sb, "sample[0..2]:\n")

	for i := range 3 {
		b, err := json.MarshalIndent(objects[i], "  ", "  ")
		if err != nil {
			return original, false
		}
		sb.WriteString("  ")
		sb.Write(b)
		sb.WriteByte('\n')
	}

	out := sb.String()
	// Meaningful compression: output is less than 3/4 of input.
	crushed := len(out) < len(original)*3/4
	return out, crushed
}

// crushObject handles Case 2: top-level object with large leaf arrays/strings.
func crushObject(original string, obj map[string]any) (string, bool) {
	modified := make(map[string]any, len(obj))
	for k, v := range obj {
		switch val := v.(type) {
		case []any:
			if len(val) > 10 {
				modified[k] = fmt.Sprintf("…(%d items)", len(val))
			} else {
				modified[k] = v
			}
		case string:
			if len(val) > 200 {
				modified[k] = fmt.Sprintf("…(%d chars)", len(val))
			} else {
				modified[k] = v
			}
		default:
			modified[k] = v
		}
	}

	b, err := json.MarshalIndent(modified, "", "  ")
	if err != nil {
		return original, false
	}

	out := string(b)
	// Meaningful compression: at least 20% smaller than the input.
	crushed := len(out) <= len(original)*4/5
	return out, crushed
}

// applySmartCrusher runs crushJSON on crushInput and, if compression occurred,
// stores storeContent (the true original, before any command-rule filtering) in
// the CCR store and returns the crushed text with a recovery footer.
// Separating crushInput from storeContent ensures that when a command rule has
// already filtered the output, the CCR entry points to the unmodified original
// rather than the intermediate rule-filtered form.
// cmd is unused today but may be used for per-command tuning.
func applySmartCrusher(_ string, crushInput, storeContent string, store *ccr.Store) (string, bool) {
	crushed, ok := crushJSON(crushInput)
	if !ok {
		return crushInput, false
	}

	handle, err := store.Put(storeContent)
	if err != nil {
		// Without a CCR handle the model has no recovery path, so fall back to
		// returning the original rather than an unrecoverable compressed form.
		log.Printf("rtk: smartcrusher: ccr put failed: %v", err)
		return crushInput, false
	}

	out := crushed + fmt.Sprintf("[JSON compressed by SmartCrusher; recover with CCRRetrieve handle=%q]", handle)
	return out, true
}

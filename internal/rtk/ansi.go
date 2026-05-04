// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import "regexp"

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[mGKHFABCDJsuhlp]|\x1b\][^\x07]*\x07|\x1b[()][\w]`)

// stripANSI removes terminal escape sequences from s.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

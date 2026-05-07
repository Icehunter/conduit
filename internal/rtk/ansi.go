// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import "regexp"

var ansiRe = regexp.MustCompile(
	`\x1b[@-Z\\-_]` + // Fe sequences: ESC + 0x40–0x5F (single-char)
		`|\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]` + // CSI: param + intermediate + final
		`|\x1b[PX^_].*?(?:\x1b\\|\x07)` + // DCS / APC / PM (ST or BEL terminated)
		`|\x1b\].*?(?:\x1b\\|\x07)` + // OSC (ST or BEL terminated)
		`|[\x9b][\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]` + // C1 CSI (0x9b)
		`|[\x9d].*?(?:\x9c|\x07)`, // C1 OSC (0x9d)
)

// stripANSI removes terminal escape sequences from s.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

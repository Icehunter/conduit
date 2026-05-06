// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import "strings"

// ── Generic helpers ───────────────────────────────────────────────────────────

func truncateLines(output string, maxLines int) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}
	dropped := len(lines) - maxLines
	return strings.Join(lines[:maxLines], "\n") + "\n[" + itoa(dropped) + " lines omitted by RTK]"
}

func filterTruncate(_ string, output string) string { return truncateLines(output, 150) }

func subcommand(cmd string) string {
	parts := strings.Fields(cmd)
	for i := 1; i < len(parts); i++ {
		if !strings.HasPrefix(parts[i], "-") {
			return parts[i]
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// keepMatching returns only lines where any predicate returns true.
func keepMatching(output string, tests ...func(string) bool) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		for _, t := range tests {
			if t(line) {
				out = append(out, line)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

func hasAny(line string, subs ...string) bool {
	l := strings.ToLower(line)
	for _, s := range subs {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func hasPrefix(line string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

package rtk

import "regexp"

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[mGKHFABCDJsuhlp]|\x1b\][^\x07]*\x07|\x1b[()][\w]`)

// stripANSI removes terminal escape sequences from s.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

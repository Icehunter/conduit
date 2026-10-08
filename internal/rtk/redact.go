package rtk

import (
	"regexp"
	"strings"
)

// secretRule masks one class of high-confidence secret. hints are lowercase
// literals of which at least one must occur (case-insensitively) in the text
// for the regex to be worth running; they keep the common no-secret case cheap.
type secretRule struct {
	kind  string
	hints []string
	re    *regexp.Regexp
	// repl builds the replacement for a match; nil replaces the whole match
	// with the placeholder.
	repl func(sub []string, placeholder string) string
}

func placeholder(kind string) string { return "[REDACTED:" + kind + "]" }

var secretRules = []secretRule{
	{
		kind:  "private-key",
		hints: []string{"private key-----"},
		// The header, then only what a key body looks like (base64 lines, PEM
		// "Proc-Type: ..." headers, blank lines), and the END line when it is
		// there. An unterminated header therefore masks the header and the key
		// lines after it, never the rest of the output.
		re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----` +
			`(?:(?:\r?\n[A-Za-z][A-Za-z0-9-]*: [^\r\n]*)*(?:\r?\n)?(?:\r?\n[A-Za-z0-9+/=]{16,})*(?:\r?\n[A-Za-z0-9+/=]+)?\r?\n-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----` +
			`|(?:\r?\n(?:[A-Za-z0-9+/=]{16,}|[A-Za-z][A-Za-z0-9-]*: [^\r\n]*))*)`),
	},
	{
		kind:  "aws-access-key",
		hints: []string{"akia", "asia", "agpa", "aida", "aroa", "anpa", "anva"},
		re:    regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA)[A-Z0-9]{16}\b`),
	},
	{
		kind:  "github-token",
		hints: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"},
		re:    regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`),
	},
	{
		kind:  "anthropic-key",
		hints: []string{"sk-ant-"},
		re:    regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`),
	},
	{
		kind:  "openai-key",
		hints: []string{"sk-"},
		re:    regexp.MustCompile(`\bsk-(?:(?:proj|svcacct|admin)-[A-Za-z0-9_\-]{32,}|[A-Za-z0-9]{20}T3BlbkFJ[A-Za-z0-9]{20}|[A-Za-z0-9]{32,})`),
	},
	{
		kind:  "slack-token",
		hints: []string{"xox"},
		re:    regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`),
	},
	{
		kind:  "stripe-key",
		hints: []string{"k_live_", "k_test_"},
		re:    regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`),
	},
	{
		kind:  "google-api-key",
		hints: []string{"aiza"},
		re:    regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),
	},
	{
		kind:  "jwt",
		hints: []string{"eyj"},
		re:    regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`),
	},
	{
		// scheme://user:password@host — mask only the password.
		kind:  "url-credentials",
		hints: []string{"://"},
		re:    regexp.MustCompile(`(\b[A-Za-z][A-Za-z0-9+.\-]*://[^\s/:@]+:)([^\s/@]+)(@)`),
		repl: func(sub []string, ph string) string {
			if strings.HasPrefix(sub[2], "[REDACTED:") {
				return sub[0]
			}
			return sub[1] + ph + sub[3]
		},
	},
	{
		// password=..., api_key: "...", client_secret = ... — mask the value.
		kind:  "credential",
		hints: []string{"password", "passwd", "secret", "api_key", "api-key", "apikey", "access_token", "access-token", "auth_token", "auth-token", "private_key", "private-key"},
		re:    regexp.MustCompile(`(?i)(\b[a-z0-9_.\-]*(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|private[_-]?key)[a-z0-9_.\-]*["']?\s*[:=]\s*["']?)([^\s"'` + "`" + `,;()\[\]{}<>$]{8,})`),
		repl: func(sub []string, ph string) string {
			// A path is not a secret even under a key named secret*.
			if strings.Contains(sub[2], "/") && strings.Contains(strings.ToLower(sub[1]), "secret") {
				return sub[0]
			}
			if !looksLikeSecretValue(sub[2]) {
				return sub[0]
			}
			return sub[1] + ph
		},
	},
}

// looksLikeSecretValue separates real assignments from code, config and test
// fixtures such as `password = hashedPassword`, `secret: string` or
// `Password: "hunter2!!"`. A secret value is long (12+ characters) and mixes
// at least three of lower case, upper case, digits and symbols, or is very
// long (24+) and mixes two, as a hex or base64 key does. An existing
// placeholder is never one.
func looksLikeSecretValue(v string) bool {
	if strings.HasPrefix(v, "[REDACTED:") || len(v) < 12 {
		return false
	}
	var lower, upper, digit, other bool
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		case r != '_' && r != '-' && r != '.':
			other = true
		}
	}
	classes := 0
	for _, c := range []bool{lower, upper, digit, other} {
		if c {
			classes++
		}
	}
	return classes >= 3 || (len(v) >= 24 && classes >= 2)
}

// RedactSecrets masks high-confidence secrets in s (cloud and API tokens,
// private key blocks, JWTs, URL credentials, and password/api_key style
// assignments) with [REDACTED:<kind>] placeholders and returns the result and
// the number of masked secrets. It works on a copy: files on disk are never
// touched, only what the model is shown changes.
func RedactSecrets(s string) (string, int) {
	if len(s) < 8 {
		return s, 0
	}
	lower := strings.ToLower(s)
	total := 0
	for i := range secretRules {
		r := &secretRules[i]
		if !containsAny(lower, r.hints) {
			continue
		}
		ph := placeholder(r.kind)
		count := 0
		if r.repl == nil {
			s = r.re.ReplaceAllStringFunc(s, func(string) string { count++; return ph })
		} else {
			s = r.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := r.re.FindStringSubmatch(m)
				out := r.repl(sub, ph)
				if out != sub[0] {
					count++
				}
				return out
			})
		}
		if count > 0 {
			total += count
			lower = strings.ToLower(s)
		}
	}
	return s, total
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

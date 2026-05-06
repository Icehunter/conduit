package bashtool

// hasShellMetachars reports whether cmd contains any unquoted metacharacters
// that could chain commands or redirect output — making an apparently
// read-only command actually modify state.
//
// Tracks single-quote and double-quote spans. Inside single-quotes nothing is
// special. Inside double-quotes, $() and “ are still active. Backslash
// escapes the next character in both double-quote and bare contexts.
//
// Returns true (= NOT safe to auto-approve) when any of the following appear
// in an unquoted position:
//   - command separators: ; && || | & \n
//   - command substitution: $( or `
//   - output redirection: >
//   - heredoc: <<
//
// Conservative: any parse ambiguity returns true.
func hasShellMetachars(cmd string) bool {
	sq := false // inside single-quote span
	dq := false // inside double-quote span
	for i := 0; i < len(cmd); {
		c := cmd[i]

		// Single-quoted span: only ' ends it; no escapes inside.
		if sq {
			if c == '\'' {
				sq = false
			}
			i++
			continue
		}

		// Opening single-quote (only outside double-quote).
		if !dq && c == '\'' {
			sq = true
			i++
			continue
		}

		// Toggle double-quote.
		if c == '"' {
			dq = !dq
			i++
			continue
		}

		// Backslash: skip the next character (in both bare and double-quote contexts).
		if c == '\\' {
			i += 2
			continue
		}

		// Command substitution: active even inside double-quotes.
		if c == '$' && i+1 < len(cmd) && cmd[i+1] == '(' {
			return true
		}
		if c == '`' {
			return true
		}

		// The remaining metacharacters only fire outside double-quotes.
		if !dq {
			switch c {
			case ';', '&', '|', '\n':
				return true
			case '>':
				return true
			case '<':
				if i+1 < len(cmd) && cmd[i+1] == '<' {
					return true // heredoc
				}
			}
		}
		i++
	}
	return false
}

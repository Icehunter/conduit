package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/icehunter/conduit/internal/rtk"
	"github.com/icehunter/conduit/internal/tools/skillmanagetool"
)

// missingRequired lists the schema's required properties that input lacks.
// It is the one schema check made on a PreToolUse hook's updatedInput; the
// tool itself validates values.
func missingRequired(schema json.RawMessage, input map[string]any) []string {
	var s struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	var missing []string
	for _, k := range s.Required {
		if _, ok := input[k]; !ok {
			missing = append(missing, k)
		}
	}
	slices.Sort(missing)
	return missing
}

// invalidUpdatedInputMsg is the denial text for a hook-rewritten input that
// lacks required properties.
func invalidUpdatedInputMsg(toolName string, missing []string) string {
	return "PreToolUse hook for " + toolName + " returned updatedInput that failed schema validation: missing required " + strings.Join(missing, ", ")
}

// redactEnabled reports whether tool results are scrubbed of secrets.
func (l *Loop) redactEnabled() bool {
	l.mu.RLock()
	f := l.cfg.RedactSecrets
	l.mu.RUnlock()
	return f == nil || f()
}

// redactResult masks secrets in a tool result before it reaches the history
// and the UI. Files on disk are untouched.
func redactResult(text string) string {
	out, _ := rtk.RedactSecrets(text)
	return out
}

// fileContentTools return file contents verbatim. Their results are not
// redacted: an edit has to match the bytes on disk, and a masked line would
// make the model write the placeholder into the file.
var fileContentTools = map[string]bool{"Read": true}

// remoteReadSchemes are the Read tool's non-file targets (web pages and
// GitHub PRs/issues); their text is not on disk, so it is redacted like any
// other tool output.
var remoteReadSchemes = []string{"http://", "https://", "pr://", "issue://"}

// returnsFileBytes reports whether a tool call's result is verbatim local file
// content and so exempt from redaction.
func returnsFileBytes(toolName string, input map[string]any) bool {
	if !fileContentTools[toolName] {
		return false
	}
	p, _ := input["file_path"].(string)
	p = strings.ToLower(strings.TrimSpace(p))
	for _, s := range remoteReadSchemes {
		if strings.HasPrefix(p, s) {
			return false
		}
	}
	return true
}

// maskedEditTools write model-supplied text into files.
var maskedEditTools = map[string]bool{"Write": true, "Edit": true, "HashEdit": true, "NotebookEdit": true, "AstGrep": true, "SkillManage": true}

var redactedToken = regexp.MustCompile(`\[REDACTED:[a-z-]+\]`)

// maskedEditMsg is the denial for a file edit that carries a redaction
// placeholder the file does not already contain.
const maskedEditMsg = "Edit rejected: it contains a [REDACTED:...] placeholder that is not in the file. Secrets are masked in tool output; edit around the masked line instead of writing the placeholder."

// carriesUnknownMask reports whether a file-editing call's input contains a
// redaction placeholder that the target file on disk does not itself contain.
// A model that copied a masked line out of tool output would otherwise write
// the placeholder over the real secret.
func carriesUnknownMask(toolName string, input map[string]any, cwd string) bool {
	if !maskedEditTools[toolName] {
		return false
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return false
	}
	tokens := redactedToken.FindAllString(string(raw), -1)
	if len(tokens) == 0 {
		return false
	}
	var path string
	if toolName == "SkillManage" {
		// file_path is skill-relative here; the SKILL.md a create/update
		// overwrites is the file to compare against. Other actions (patch,
		// write_file) have no single target, so any placeholder is rejected.
		action, _ := input["action"].(string)
		name, _ := input["name"].(string)
		scope, _ := input["scope"].(string)
		path = skillmanagetool.TargetPath(cwdOrProcess(cwd), action, name, scope)
		if path == "" {
			return true
		}
	}
	for _, k := range []string{"file_path", "notebook_path", "path"} {
		if path != "" {
			break
		}
		if p, ok := input[k].(string); ok && p != "" {
			path = p
			break
		}
	}
	if path == "" {
		return true
	}
	if !filepath.IsAbs(path) && cwd != "" {
		path = filepath.Join(cwd, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	for _, tok := range tokens {
		if !strings.Contains(string(content), tok) {
			return true
		}
	}
	return false
}

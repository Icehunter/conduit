package fileedittool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A scheme-like prefix makes ResolvePath return the input uncleaned and
// relative; Edit must refuse it rather than resolve it against the process cwd.
func TestEdit_RejectsNonAbsoluteAfterResolve(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"file_path": "ab:/../sub/x", "old_string": "a", "new_string": "b"})
	res, err := New().Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "must be absolute") {
		t.Fatalf("got IsError=%v %q", res.IsError, res.Content[0].Text)
	}
}

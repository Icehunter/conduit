package undercover

import (
	"os"
	"testing"
)

func TestIsUndercover_Default(t *testing.T) {
	os.Unsetenv("CLAUDE_CODE_UNDERCOVER")
	// External builds: always false by default.
	if IsUndercover() {
		t.Error("expected IsUndercover()=false by default in external build")
	}
}

func TestIsUndercover_EnvForceOn(t *testing.T) {
	os.Setenv("CLAUDE_CODE_UNDERCOVER", "1")
	defer os.Unsetenv("CLAUDE_CODE_UNDERCOVER")
	if !IsUndercover() {
		t.Error("expected IsUndercover()=true when CLAUDE_CODE_UNDERCOVER=1")
	}
}

func TestIsUndercover_EnvTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on"} {
		os.Setenv("CLAUDE_CODE_UNDERCOVER", v)
		if !IsUndercover() {
			t.Errorf("expected IsUndercover()=true for CLAUDE_CODE_UNDERCOVER=%q", v)
		}
	}
	os.Unsetenv("CLAUDE_CODE_UNDERCOVER")
}

func TestIsUndercover_EnvFalsy(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off", ""} {
		os.Setenv("CLAUDE_CODE_UNDERCOVER", v)
		if IsUndercover() {
			t.Errorf("expected IsUndercover()=false for CLAUDE_CODE_UNDERCOVER=%q", v)
		}
	}
	os.Unsetenv("CLAUDE_CODE_UNDERCOVER")
}

func TestGetUndercoverInstructions_WhenOff(t *testing.T) {
	os.Unsetenv("CLAUDE_CODE_UNDERCOVER")
	if s := GetUndercoverInstructions(); s != "" {
		t.Errorf("expected empty instructions when undercover off; got %q", s)
	}
}

func TestGetUndercoverInstructions_WhenOn(t *testing.T) {
	os.Setenv("CLAUDE_CODE_UNDERCOVER", "1")
	defer os.Unsetenv("CLAUDE_CODE_UNDERCOVER")
	s := GetUndercoverInstructions()
	if s == "" {
		t.Error("expected non-empty instructions when undercover on")
	}
	// Must warn about not leaking AI attribution.
	for _, must := range []string{"UNDERCOVER", "commit", "Co-Authored-By"} {
		found := false
		for i := 0; i+len(must) <= len(s); i++ {
			if s[i:i+len(must)] == must {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("instructions should mention %q", must)
		}
	}
}

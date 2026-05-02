package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type buildAuthURLCase struct {
	Name  string `json:"name"`
	Input struct {
		CodeChallenge     string `json:"code_challenge"`
		State             string `json:"state"`
		Port              int    `json:"port"`
		IsManual          bool   `json:"is_manual"`
		LoginWithClaudeAI bool   `json:"login_with_claude_ai"`
		InferenceOnly     bool   `json:"inference_only"`
		OrgUUID           string `json:"org_uuid"`
		LoginHint         string `json:"login_hint"`
		LoginMethod       string `json:"login_method"`
	} `json:"input"`
	ExpectedURL string `json:"expected_url"`
}

type buildAuthURLFixture struct {
	Cases []buildAuthURLCase `json:"cases"`
}

// TestBuildAuthURL_Fixtures verifies our URL builder produces byte-identical
// output to decoded/1220.js function GI_ for a battery of inputs.
//
// The order of search-param appending matters because the reference uses
// repeated URLSearchParams.append(), which preserves insertion order; some
// servers validate against expected query strings so we must match exactly.
func TestBuildAuthURL_Fixtures(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "fixtures", "auth", "build_auth_url_cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx buildAuthURLFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture had no cases")
	}

	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := BuildAuthURL(ProdConfig, BuildAuthURLParams{
				CodeChallenge:     c.Input.CodeChallenge,
				State:             c.Input.State,
				Port:              c.Input.Port,
				IsManual:          c.Input.IsManual,
				LoginWithClaudeAI: c.Input.LoginWithClaudeAI,
				InferenceOnly:     c.Input.InferenceOnly,
				OrgUUID:           c.Input.OrgUUID,
				LoginHint:         c.Input.LoginHint,
				LoginMethod:       c.Input.LoginMethod,
			})
			if err != nil {
				t.Fatalf("BuildAuthURL: %v", err)
			}
			if got != c.ExpectedURL {
				t.Fatalf("BuildAuthURL mismatch\n got: %s\nwant: %s", got, c.ExpectedURL)
			}
		})
	}
}

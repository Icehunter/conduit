package api

import (
	"reflect"
	"testing"
)

func TestFilterBetasForModel(t *testing.T) {
	allBetas := []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
		"context-1m-2025-08-07",
		"effort-2025-11-24",
	}

	tests := []struct {
		name  string
		model string
		want  []string
	}{
		{
			name:  "sonnet passes all betas",
			model: "claude-sonnet-4-6",
			want:  allBetas,
		},
		{
			name:  "opus passes all betas",
			model: "claude-opus-4-7",
			want:  allBetas,
		},
		{
			name:  "haiku strips context-1m and interleaved-thinking",
			model: "claude-haiku-4-5-20251001",
			want: []string{
				"claude-code-20250219",
				"oauth-2025-04-20",
				"context-management-2025-06-27",
				"effort-2025-11-24",
			},
		},
		{
			name:  "haiku 3.5 also stripped",
			model: "claude-3-5-haiku-20241022",
			want: []string{
				"claude-code-20250219",
				"oauth-2025-04-20",
				"context-management-2025-06-27",
				"effort-2025-11-24",
			},
		},
		{
			name:  "haiku 3 also stripped",
			model: "claude-3-haiku-20240307",
			want: []string{
				"claude-code-20250219",
				"oauth-2025-04-20",
				"context-management-2025-06-27",
				"effort-2025-11-24",
			},
		},
		{
			name:  "empty model passes all",
			model: "",
			want:  allBetas,
		},
		{
			name:  "unknown model passes all",
			model: "some-other-provider/model-v1",
			want:  allBetas,
		},
		{
			name:  "models/ path prefix stripped before match",
			model: "models/claude-haiku-4-5-20251001",
			want: []string{
				"claude-code-20250219",
				"oauth-2025-04-20",
				"context-management-2025-06-27",
				"effort-2025-11-24",
			},
		},
		{
			name:  "empty betas slice returns non-nil empty",
			model: "claude-haiku-4-5-20251001",
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := allBetas
			if tt.name == "empty betas slice returns non-nil empty" {
				input = []string{}
			}
			got := filterBetasForModel(input, tt.model)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("filterBetasForModel(%v) = %v; want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestFilterBetasForModel_NilInput(t *testing.T) {
	got := filterBetasForModel(nil, "claude-haiku-4-5-20251001")
	if got == nil {
		t.Errorf("expected non-nil slice, got nil")
	}
}

func TestFilterBetasForModel_HaikuBetasSuppressedInRequest(t *testing.T) {
	// Integration-style: confirm that the two known-bad headers are absent
	// for any model matching the haiku prefix, regardless of date suffix.
	haikuModels := []string{
		"claude-haiku-4-5-20251001",
		"claude-3-5-haiku-20241022",
		"claude-3-haiku-20240307",
		"claude-haiku-4-5",
	}
	disallowed := []string{
		"context-1m-2025-08-07",
		"interleaved-thinking-2025-05-14",
	}
	betas := []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"context-1m-2025-08-07",
	}
	for _, model := range haikuModels {
		got := filterBetasForModel(betas, model)
		for _, bad := range disallowed {
			for _, b := range got {
				if b == bad {
					t.Errorf("model %q: beta %q should be suppressed but was passed through", model, bad)
				}
			}
		}
	}
}

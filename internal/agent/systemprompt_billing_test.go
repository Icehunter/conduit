package agent

import (
	"strings"
	"testing"
)

// TestComputeBillingSuffix_MatchesLiveCapture pins the cc_version suffix
// algorithm to the value the real CLI produced for the same prompt in the
// 2.1.266 live capture (scripts/wire-check/history/2.1.266/live-capture.json).
// The version is passed explicitly so a BillingVersion bump doesn't break it;
// re-verify the algorithm against the decoded bundle on each wire sync.
func TestComputeBillingSuffix_MatchesLiveCapture(t *testing.T) {
	if got := billingSuffixFor("say the word hello and nothing else", "2.1.266"); got != "0f4" {
		t.Fatalf("billingSuffixFor = %q; want %q (live capture for 2.1.266)", got, "0f4")
	}
}

func TestDynamicBillingBlock(t *testing.T) {
	t.Setenv("CLAUDE_GO_BILLING_HEADER", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	const prompt = "say the word hello and nothing else"
	base := "x-anthropic-billing-header: cc_version=" + BillingVersion + "." + computeBillingSuffix(prompt) + "; cc_entrypoint=sdk-cli; cch=" + BillingCch + ";"
	tests := []struct {
		name string
		bc   BillingContext
		want string
	}{
		{"no context", BillingContext{}, base + "\n"},
		{"prompt id", BillingContext{PromptID: "964702b8-08db-4077-ba04-bfadc6600035"},
			base + " cc_prompt_id=964702b8-08db-4077-ba04-bfadc6600035;\n"},
		{"prev req and prompt id, CC order", BillingContext{PromptID: "964702b8-08db-4077-ba04-bfadc6600035", PrevRequestID: "req_011CesBWTayGqSS3pBGc1vjc"},
			base + " cc_prev_req=req_011CesBWTayGqSS3pBGc1vjc; cc_prompt_id=964702b8-08db-4077-ba04-bfadc6600035;\n"},
		{"subagent first", BillingContext{IsSubAgent: true, PromptID: "964702b8-08db-4077-ba04-bfadc6600035"},
			base + " cc_is_subagent=true; cc_prompt_id=964702b8-08db-4077-ba04-bfadc6600035;\n"},
		{"malformed ids dropped", BillingContext{PromptID: "not-a-uuid", PrevRequestID: "nope"}, base + "\n"},
		{"prev req too long dropped", BillingContext{PrevRequestID: "req_" + strings.Repeat("a", 37)}, base + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DynamicBillingBlock(prompt, tt.bc)
			if got.Type != "text" || got.Text != tt.want {
				t.Errorf("got  %q\nwant %q", got.Text, tt.want)
			}
		})
	}
}

func TestDynamicBillingBlock_EnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_GO_BILLING_HEADER", "x-anthropic-billing-header: custom;\n")
	got := DynamicBillingBlock("anything", BillingContext{PromptID: "964702b8-08db-4077-ba04-bfadc6600035"})
	if got.Text != "x-anthropic-billing-header: custom;\n" {
		t.Errorf("env override ignored: %q", got.Text)
	}
}

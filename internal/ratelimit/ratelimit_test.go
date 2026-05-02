package ratelimit

import (
	"net/http"
	"testing"
)

func TestParse_Empty(t *testing.T) {
	h := http.Header{}
	info := Parse(h)
	if info.HasData() {
		t.Error("empty header should yield no data")
	}
}

func TestParse_RequestsRemaining(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-remaining", "500")
	h.Set("anthropic-ratelimit-requests-limit", "1000")

	info := Parse(h)
	if !info.HasData() {
		t.Fatal("should have data")
	}
	if info.RequestsRemaining != 500 {
		t.Errorf("RequestsRemaining = %d; want 500", info.RequestsRemaining)
	}
	if info.RequestsLimit != 1000 {
		t.Errorf("RequestsLimit = %d; want 1000", info.RequestsLimit)
	}
}

func TestParse_TokensRemaining(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-tokens-remaining", "40000")
	h.Set("anthropic-ratelimit-tokens-limit", "100000")

	info := Parse(h)
	if info.TokensRemaining != 40000 {
		t.Errorf("TokensRemaining = %d; want 40000", info.TokensRemaining)
	}
}

func TestParse_InputTokensRemaining(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-input-tokens-remaining", "8000")
	h.Set("anthropic-ratelimit-input-tokens-limit", "40000")

	info := Parse(h)
	if info.InputTokensRemaining != 8000 {
		t.Errorf("InputTokensRemaining = %d; want 8000", info.InputTokensRemaining)
	}
	if info.InputTokensLimit != 40000 {
		t.Errorf("InputTokensLimit = %d; want 40000", info.InputTokensLimit)
	}
}

func TestWarningMessage_NoData(t *testing.T) {
	info := Info{}
	if msg := info.WarningMessage(); msg != "" {
		t.Errorf("WarningMessage = %q; want empty", msg)
	}
}

func TestWarningMessage_UnderThreshold(t *testing.T) {
	info := Info{
		RequestsRemaining: 800,
		RequestsLimit:     1000,
	}
	// 80% remaining — no warning
	if msg := info.WarningMessage(); msg != "" {
		t.Errorf("WarningMessage = %q; want empty for high remaining", msg)
	}
}

func TestWarningMessage_NearLimit(t *testing.T) {
	info := Info{
		RequestsRemaining: 50,
		RequestsLimit:     1000,
		TokensRemaining:   500,
		TokensLimit:       100000,
	}
	// 5% remaining → should warn
	msg := info.WarningMessage()
	if msg == "" {
		t.Error("WarningMessage should warn when <20% remaining")
	}
}

func TestPctRemaining_ZeroLimit(t *testing.T) {
	info := Info{
		RequestsRemaining: 0,
		RequestsLimit:     0,
	}
	// Zero limit → no warning (avoid div-by-zero)
	if msg := info.WarningMessage(); msg != "" {
		t.Errorf("WarningMessage = %q; want empty for zero limit", msg)
	}
}

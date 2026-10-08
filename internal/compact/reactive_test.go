package compact

import (
	"context"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

func TestDefaultModel(t *testing.T) {
	if DefaultModel != "claude-haiku-5-5" {
		t.Errorf("DefaultModel = %q, want claude-haiku-5-5 (CC 2.1.293 latest_per_family.haiku)", DefaultModel)
	}
}

func TestCompactWithOptions_ReactiveSendsAutoOnTheWire(t *testing.T) {
	f := &fakeSummarizer{text: "<summary>S</summary>"}
	res, err := CompactWithOptions(context.Background(), f.client(t), convo(), Options{Kind: KindReactive})
	if err != nil {
		t.Fatalf("CompactWithOptions: %v", err)
	}
	if len(res.NewHistory) == 0 {
		t.Fatal("empty history")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hints) != 1 || f.hints[0].Compaction != KindAuto || f.hints[0].RequestClass != "compaction" {
		t.Errorf("hints = %+v, want one compaction request with Compaction=auto", f.hints)
	}
}

func TestCompactLocal(t *testing.T) {
	t.Run("builds a non-empty history without a model", func(t *testing.T) {
		res, err := CompactLocal(convo(), Options{Kind: KindReactive, MainWindow: 200_000})
		if err != nil {
			t.Fatalf("CompactLocal: %v", err)
		}
		if !res.Fallback || res.Summary == "" || len(res.NewHistory) < 1 {
			t.Fatalf("result = fallback %v summary %q history %d", res.Fallback, res.Summary, len(res.NewHistory))
		}
		first := onlyText(res.NewHistory[0])
		if !strings.HasPrefix(first, "<summary>") || !strings.Contains(first, "please fix internal/foo/bar.go") {
			t.Errorf("summary message = %q, want the summary and the verbatim user request", first)
		}
		if res.TailLen != len(res.NewHistory)-1 {
			t.Errorf("TailLen = %d with %d messages", res.TailLen, len(res.NewHistory))
		}
	})
	t.Run("keeps a previous summary", func(t *testing.T) {
		f := &fakeSummarizer{text: "<summary>OLD SUMMARY</summary>"}
		first, err := CompactWithOptions(context.Background(), f.client(t), convo(), Options{TailTokens: 1})
		if err != nil {
			t.Fatal(err)
		}
		hist := append(first.NewHistory, umsg("more work"), amsg("more done"), umsg("and more"), amsg("tail"))
		res, err := CompactLocal(hist, Options{TailTokens: 1})
		if err != nil {
			t.Fatalf("CompactLocal: %v", err)
		}
		if !strings.Contains(res.Summary, "OLD SUMMARY") {
			t.Errorf("summary = %q, want the previous summary kept", res.Summary)
		}
	})
}

func TestCompactLocal_Errors(t *testing.T) {
	if _, err := CompactLocal(nil, Options{}); err == nil {
		t.Error("empty history: want an error")
	}
	_, err := CompactLocal(convo()[5:], Options{})
	if !IsNothingToCompact(err) {
		t.Errorf("history that is all tail: err = %v, want nothing-to-compact", err)
	}
}

func TestRequireTail(t *testing.T) {
	lone := []api.Message{umsg("one enormous prompt")}
	tests := []struct {
		name string
		msgs []api.Message
		req  bool
		want bool // nothing to compact
	}{
		{"lone prompt without the option compacts", lone, false, false},
		{"lone prompt with the option is nothing to compact", lone, true, true},
		{"history with an assistant turn compacts", convo(), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSummarizer{text: "<summary>S</summary>"}
			_, err := CompactWithOptions(context.Background(), f.client(t), tt.msgs, Options{RequireTail: tt.req})
			if IsNothingToCompact(err) != tt.want {
				t.Errorf("CompactWithOptions err = %v, nothing-to-compact want %v", err, tt.want)
			}
			if tt.want && f.requests != 0 {
				t.Errorf("summarizer was called %d times", f.requests)
			}
			_, lerr := CompactLocal(tt.msgs, Options{RequireTail: tt.req})
			if IsNothingToCompact(lerr) != tt.want {
				t.Errorf("CompactLocal err = %v, nothing-to-compact want %v", lerr, tt.want)
			}
		})
	}
}

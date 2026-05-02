package sleeptool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func input(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(v)
	return b
}

func TestSleep_ShortDuration(t *testing.T) {
	tt := New()
	start := time.Now()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"duration_seconds": 0.05}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("didn't sleep long enough: %v", elapsed)
	}
	if !strings.Contains(res.Content[0].Text, "Slept") {
		t.Errorf("unexpected result: %s", res.Content[0].Text)
	}
}

func TestSleep_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tt := New()
	res, err := tt.Execute(ctx, input(t, map[string]any{"duration_seconds": 60}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("cancelled context should produce IsError=true")
	}
}

func TestSleep_ZeroDuration(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"duration_seconds": 0}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("zero duration should be IsError=true")
	}
}

func TestSleep_NegativeDuration(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"duration_seconds": -1}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("negative duration should be IsError=true")
	}
}

func TestSleep_ExceedsMax(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"duration_seconds": 301}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("duration > max should be IsError=true")
	}
}

func TestSleep_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestSleep_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "Sleep" {
		t.Errorf("Name = %q", tt.Name())
	}
	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should be true")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
}

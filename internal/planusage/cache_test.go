package planusage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadCache(t *testing.T) {
	dir := t.TempDir()

	now := time.Now().Truncate(time.Second)
	backoff := now.Add(5 * time.Minute)
	entry := CacheEntry{
		Info: Info{
			FiveHour: Window{Utilization: 0.42, ResetsAt: now.Add(time.Hour)},
			SevenDay: Window{Utilization: 0.10, ResetsAt: now.Add(7 * 24 * time.Hour)},
		},
		CachedAt:     now,
		BackoffUntil: backoff,
	}

	if err := SaveCache(dir, entry); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	got, err := LoadCache(dir)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if !got.CachedAt.Equal(entry.CachedAt) {
		t.Errorf("CachedAt: got %v want %v", got.CachedAt, entry.CachedAt)
	}
	if !got.BackoffUntil.Equal(entry.BackoffUntil) {
		t.Errorf("BackoffUntil: got %v want %v", got.BackoffUntil, entry.BackoffUntil)
	}
	if got.Info.FiveHour.Utilization != entry.Info.FiveHour.Utilization {
		t.Errorf("FiveHour.Utilization: got %v want %v", got.Info.FiveHour.Utilization, entry.Info.FiveHour.Utilization)
	}
}

func TestLoadCache_Missing(t *testing.T) {
	dir := t.TempDir()
	entry, err := LoadCache(dir)
	if err != nil {
		t.Fatalf("LoadCache on missing file returned error: %v", err)
	}
	if !entry.CachedAt.IsZero() {
		t.Errorf("expected zero CachedAt for missing cache, got %v", entry.CachedAt)
	}
}

func TestLoadCacheForKeyWithFallback(t *testing.T) {
	dir := t.TempDir()
	legacyDir := t.TempDir()
	key := "claude-subscription.work@example.com.claude-opus-4-7"
	entry := CacheEntry{CachedAt: time.Now().Truncate(time.Second)}
	if err := SaveCacheForKey(legacyDir, key, entry); err != nil {
		t.Fatalf("SaveCacheForKey legacy: %v", err)
	}

	got, err := LoadCacheForKeyWithFallback(dir, legacyDir, key)
	if err != nil {
		t.Fatalf("LoadCacheForKeyWithFallback: %v", err)
	}
	if !got.CachedAt.Equal(entry.CachedAt) {
		t.Fatalf("CachedAt = %v, want %v", got.CachedAt, entry.CachedAt)
	}
}

func TestLoadCacheForKeyWithFallbackKeepsBackoffOnlyEntry(t *testing.T) {
	dir := t.TempDir()
	legacyDir := t.TempDir()
	key := "claude-subscription.work@example.com.claude-opus-4-7"
	entry := CacheEntry{BackoffUntil: time.Now().Add(5 * time.Minute).Truncate(time.Second)}
	if err := SaveCacheForKey(dir, key, entry); err != nil {
		t.Fatalf("SaveCacheForKey: %v", err)
	}
	legacy := CacheEntry{CachedAt: time.Now().Truncate(time.Second)}
	if err := SaveCacheForKey(legacyDir, key, legacy); err != nil {
		t.Fatalf("SaveCacheForKey legacy: %v", err)
	}

	got, err := LoadCacheForKeyWithFallback(dir, legacyDir, key)
	if err != nil {
		t.Fatalf("LoadCacheForKeyWithFallback: %v", err)
	}
	if !got.BackoffUntil.Equal(entry.BackoffUntil) {
		t.Fatalf("BackoffUntil = %v, want %v", got.BackoffUntil, entry.BackoffUntil)
	}
	if !got.CachedAt.IsZero() {
		t.Fatalf("CachedAt = %v, want zero backoff-only entry", got.CachedAt)
	}
}

func TestLoadCache_Corrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheFile), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCache(dir)
	if err == nil {
		t.Fatal("expected error for corrupt cache, got nil")
	}
}

func TestSaveCache_Atomic(t *testing.T) {
	dir := t.TempDir()
	entry := CacheEntry{CachedAt: time.Now()}

	if err := SaveCache(dir, entry); err != nil {
		t.Fatalf("first SaveCache: %v", err)
	}
	if err := SaveCache(dir, entry); err != nil {
		t.Fatalf("second SaveCache (overwrite): %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != cacheFile {
			t.Errorf("unexpected leftover temp file: %s", e.Name())
		}
	}
}

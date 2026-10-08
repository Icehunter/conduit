package readtracker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	tests := []struct {
		name  string
		setup func(tr *Tracker)
		disk  string
		want  error
	}{
		{"never read", func(*Tracker) {}, "v1", ErrNotRead},
		{"read and unchanged", func(tr *Tracker) { tr.RecordRead(p, []byte("v1"), nil) }, "v1", nil},
		{"read then changed", func(tr *Tracker) { tr.RecordRead(p, []byte("v1"), nil) }, "v2", ErrChanged},
		{"reread picks up change", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), nil)
			tr.RecordRead(p, []byte("v2"), nil)
		}, "v2", nil},
		{"old version no longer accepted", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), nil)
			tr.RecordRead(p, []byte("v2"), nil)
		}, "v1", ErrChanged},
		{"own write counts as read", func(tr *Tracker) { tr.RecordWrite(p, []byte("w")) }, "w", nil},
		{"own write replaces read", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), nil)
			tr.RecordWrite(p, []byte("w"))
		}, "v1", ErrChanged},
		{"staged accepts disk and staged", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), nil)
			tr.RecordStaged(p, []byte("s"))
		}, "s", nil},
		{"staged keeps disk version", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), nil)
			tr.RecordStaged(p, []byte("s"))
		}, "v1", nil},
		{"reset forgets", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), nil)
			tr.Reset()
		}, "v1", ErrNotRead},
		{"nil tracker permits", nil, "anything", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tr *Tracker
			if tt.setup != nil {
				tr = New()
				tt.setup(tr)
			}
			err := tr.Check(p, []byte(tt.disk))
			if !errors.Is(err, tt.want) {
				t.Fatalf("Check = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestShown(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	tests := []struct {
		name  string
		setup func(tr *Tracker)
		want  bool
	}{
		{"unread", func(*Tracker) {}, false},
		{"plain read shows nothing", func(tr *Tracker) { tr.RecordRead(p, []byte("v1"), nil) }, false},
		{"anchored read", func(tr *Tracker) { tr.RecordRead(p, []byte("v1"), []string{"abc"}) }, true},
		{"anchors accumulate across reads of same content", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), []string{"zzz"})
			tr.RecordRead(p, []byte("v1"), []string{"abc"})
		}, true},
		{"changed content drops old anchors", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), []string{"abc"})
			tr.RecordRead(p, []byte("v2"), nil)
		}, false},
		{"write keeps anchors", func(tr *Tracker) {
			tr.RecordRead(p, []byte("v1"), []string{"abc"})
			tr.RecordWrite(p, []byte("v2"))
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := New()
			tt.setup(tr)
			if got := tr.Shown(p, "abc"); got != tt.want {
				t.Fatalf("Shown = %v, want %v", got, tt.want)
			}
		})
	}
	var nilTr *Tracker
	if !nilTr.Shown(p, "abc") {
		t.Fatal("nil tracker must permit")
	}
}

// The key must not change when the file is created under a symlinked
// directory (macOS /var -> /private/var).
func TestKeyStableAcrossCreationUnderSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(link, "new.txt")
	before := key(p)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := key(p); after != before {
		t.Fatalf("key changed once file existed: %q -> %q", before, after)
	}
	tr := New()
	tr.RecordStaged(p, []byte("x"))
	if err := tr.Check(p, []byte("x")); err != nil {
		t.Fatalf("Check after create: %v", err)
	}
}

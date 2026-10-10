package attach

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadImageFile_SetsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.png")
	if err := os.WriteFile(p, []byte("not really a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := ReadImageFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if img.Path != p {
		t.Errorf("Path = %q, want %q", img.Path, p)
	}
}

func TestPersistImage(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		wantFile bool
	}{
		{"valid", base64.StdEncoding.EncodeToString([]byte("png bytes")), true},
		{"empty", "", false},
		{"not base64", "!!!", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
			img := &Image{Data: tt.data, MediaType: "image/png"}
			persistImage(img)
			if (img.Path != "") != tt.wantFile {
				t.Fatalf("Path = %q, wantFile %v", img.Path, tt.wantFile)
			}
			if !tt.wantFile {
				return
			}
			st, err := os.Stat(img.Path)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o600 {
				t.Errorf("file mode = %v, want 0600", st.Mode().Perm())
			}
			dst, err := os.Stat(filepath.Dir(img.Path))
			if err != nil {
				t.Fatal(err)
			}
			if dst.Mode().Perm() != 0o700 {
				t.Errorf("dir mode = %v, want 0700", dst.Mode().Perm())
			}
		})
	}
}

func TestPersistImage_DedupesByContent(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	enc := base64.StdEncoding.EncodeToString([]byte("same bytes"))
	a, b := &Image{Data: enc}, &Image{Data: enc}
	persistImage(a)
	persistImage(b)
	if a.Path == "" || a.Path != b.Path {
		t.Fatalf("paths %q and %q, want one shared file", a.Path, b.Path)
	}
	other := &Image{Data: base64.StdEncoding.EncodeToString([]byte("different"))}
	persistImage(other)
	entries, err := os.ReadDir(filepath.Dir(a.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || other.Path == a.Path {
		t.Errorf("%d files, want 2 (no temp leftovers)", len(entries))
	}
}

func TestPrunePastedImages(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write("old.png", 8*24*time.Hour)
	write("edge.png", 7*24*time.Hour)
	write("new.png", time.Hour)
	if err := prunePastedImages(dir, now, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"old.png": false, "edge.png": true, "new.png": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", name, err == nil, want)
		}
	}
	if err := prunePastedImages(filepath.Join(dir, "missing"), now, time.Hour); err != nil {
		t.Errorf("missing dir: %v", err)
	}
}

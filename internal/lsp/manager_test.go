package lsp

import (
	"context"
	"testing"
)

// ---- languageKey -----------------------------------------------------------

func TestLanguageKey(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".ts", "typescript"},
		{".tsx", "typescript"},
		{".js", "typescript"},
		{".jsx", "typescript"},
		{".go", "go"},
		{".py", "python"},
		{".rs", "rust"},
		{".vue", "vue"},
		{".svelte", "svelte"},
		{".yaml", "yaml"},
		{".yml", "yaml"},
		{".sh", "bash"},
		{".bash", "bash"},
		{".tf", "terraform"},
		{".lua", "lua"},
		{".cs", "csharp"},
		{".java", "java"},
		{".nix", "nix"},
	}
	for _, tt := range tests {
		if got := languageKey(tt.ext); got != tt.want {
			t.Errorf("languageKey(%q) = %q; want %q", tt.ext, got, tt.want)
		}
	}
}

// ---- LanguageID ------------------------------------------------------------

func TestLanguageID(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".go", "go"},
		{".ts", "typescript"},
		{".tsx", "typescriptreact"},
		{".js", "javascript"},
		{".jsx", "javascriptreact"},
		{".py", "python"},
		{".rs", "rust"},
		{".vue", "vue"},
		{".svelte", "svelte"},
		{".yaml", "yaml"},
		{".yml", "yaml"},
		{".sh", "shellscript"},
		{".bash", "shellscript"},
		{".lua", "lua"},
		{".cs", "csharp"},
		{".java", "java"},
		{".tf", "terraform"},
		{".nix", "nix"},
		{".unknown", "plaintext"},
	}
	for _, tt := range tests {
		if got := LanguageID(tt.ext); got != tt.want {
			t.Errorf("LanguageID(%q) = %q; want %q", tt.ext, got, tt.want)
		}
	}
}

// ---- resolveSpec -----------------------------------------------------------

func TestResolveSpec_notFound(t *testing.T) {
	_, err := resolveSpec(serverSpec{cmd: "definitely-not-a-binary-xyz-abc"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// ---- Manager.ServerFor -----------------------------------------------------

func TestManager_unknownExtension(t *testing.T) {
	m := NewManager()
	defer m.Close()
	_, err := m.ServerFor(context.Background(), "/tmp/file.unknown_xyz")
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
}

func TestManager_disabledServer(t *testing.T) {
	m := NewManagerWithOverrides(map[string]ServerOverride{
		"go": {Disabled: true},
	})
	defer m.Close()
	_, err := m.ServerFor(context.Background(), "/tmp/main.go")
	if err == nil {
		t.Fatal("expected error for disabled server")
	}
}

func TestManager_overrideCmd(t *testing.T) {
	// An override pointing to a nonexistent binary should surface an error,
	// proving the override is used instead of the default spec.
	m := NewManagerWithOverrides(map[string]ServerOverride{
		"go": {Cmd: "definitely-not-on-path-xyz"},
	})
	defer m.Close()
	_, err := m.ServerFor(context.Background(), "/tmp/main.go")
	if err == nil {
		t.Fatal("expected error: override binary should not be found")
	}
}

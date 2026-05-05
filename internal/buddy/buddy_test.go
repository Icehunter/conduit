package buddy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMulberry32_Deterministic(t *testing.T) {
	rng1 := mulberry32(12345)
	rng2 := mulberry32(12345)
	for i := 0; i < 20; i++ {
		a, b := rng1(), rng2()
		if a != b {
			t.Fatalf("mulberry32 not deterministic at step %d: %v vs %v", i, a, b)
		}
	}
}

func TestMulberry32_Range(t *testing.T) {
	rng := mulberry32(0xdeadbeef)
	for i := 0; i < 1000; i++ {
		v := rng()
		if v < 0 || v >= 1 {
			t.Fatalf("mulberry32 out of [0,1): %v", v)
		}
	}
}

func TestMulberry32_DifferentSeeds(t *testing.T) {
	a := mulberry32(1)()
	b := mulberry32(2)()
	if a == b {
		t.Error("different seeds should produce different values")
	}
}

func TestFNV1a_Consistent(t *testing.T) {
	h1 := fnv1a("hello")
	h2 := fnv1a("hello")
	if h1 != h2 {
		t.Error("fnv1a not consistent")
	}
	if fnv1a("hello") == fnv1a("world") {
		t.Error("different strings should (almost certainly) hash differently")
	}
}

func TestGenerateBones_Deterministic(t *testing.T) {
	b1 := GenerateBones("user-123")
	b2 := GenerateBones("user-123")
	if b1.Species != b2.Species || b1.Rarity != b2.Rarity || b1.Eye != b2.Eye || b1.Hat != b2.Hat {
		t.Errorf("GenerateBones not deterministic: %+v vs %+v", b1, b2)
	}
}

func TestGenerateBones_ValidSpecies(t *testing.T) {
	for i := 0; i < 50; i++ {
		b := GenerateBones(string(rune('a' + i)))
		found := false
		for _, s := range AllSpecies {
			if s == b.Species {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("invalid species %q", b.Species)
		}
	}
}

func TestGenerateBones_ValidRarity(t *testing.T) {
	rarities := map[string]bool{"common": true, "uncommon": true, "rare": true, "epic": true, "legendary": true}
	for i := 0; i < 50; i++ {
		b := GenerateBones(string(rune('a' + i)))
		if !rarities[b.Rarity] {
			t.Errorf("invalid rarity %q", b.Rarity)
		}
	}
}

func TestGenerateBones_StatsInRange(t *testing.T) {
	b := GenerateBones("test-user")
	for name, val := range b.Stats {
		if val < 1 || val > 100 {
			t.Errorf("stat %s=%d out of [1,100]", name, val)
		}
	}
}

func TestRenderSprite_ReturnsString(t *testing.T) {
	b := GenerateBones("user-abc")
	sprite := RenderSprite(b, 0)
	if sprite == "" {
		t.Error("RenderSprite returned empty string")
	}
}

func TestRenderSprite_FrameWraps(t *testing.T) {
	b := GenerateBones("user-abc")
	// Frame count should wrap (no panic on large frame number).
	_ = RenderSprite(b, 999)
}

func TestRenderFace_TwoChars(t *testing.T) {
	b := GenerateBones("user-abc")
	face := RenderFace(b)
	if face == "" {
		t.Error("RenderFace returned empty string")
	}
}

func TestSpriteFrameCount_Positive(t *testing.T) {
	for _, s := range AllSpecies {
		c := SpriteFrameCount(s)
		if c < 1 {
			t.Errorf("species %q has %d frames, want >= 1", s, c)
		}
	}
}

func TestAllSpecies_Count(t *testing.T) {
	if len(AllSpecies) != 18 {
		t.Errorf("expected 18 species; got %d", len(AllSpecies))
	}
}

func TestForceRarity_Legendary(t *testing.T) {
	t.Setenv("CLAUDE_BUDDY_FORCE_RARITY", "legendary")
	b := GenerateBones("any-user")
	if b.Rarity != "legendary" {
		t.Errorf("expected legendary; got %q", b.Rarity)
	}
	// Species/eye/hat should still vary by user ID.
	b2 := GenerateBones("different-user")
	if b.Species == b2.Species && b.Eye == b2.Eye && b.Hat == b2.Hat {
		t.Error("different users should still get different appearance despite forced rarity")
	}
}

func TestForceRarity_InvalidFallsBack(t *testing.T) {
	t.Setenv("CLAUDE_BUDDY_FORCE_RARITY", "mythic")
	// Invalid rarity should be ignored, normal weighted roll applies.
	b := GenerateBones("user-x")
	validRarities := map[string]bool{"common": true, "uncommon": true, "rare": true, "epic": true, "legendary": true}
	if !validRarities[b.Rarity] {
		t.Errorf("invalid forced rarity should fall back to valid roll; got %q", b.Rarity)
	}
}

func TestForceRarity_StatsUseCorrectFloor(t *testing.T) {
	t.Setenv("CLAUDE_BUDDY_FORCE_RARITY", "legendary")
	b := GenerateBones("user-stats-test")
	floor := rarityFloor["legendary"] // 50
	for stat, val := range b.Stats {
		// Peak stat can go up to floor+79=129 (capped at 100), dump stat can go to floor-10=40.
		// All stats should be above the legendary dump floor minimum.
		if val < floor-10 {
			t.Errorf("stat %s=%d below legendary dump floor %d", stat, val, floor-10)
		}
	}
}

func TestSave_PreservesUnknownFieldsAndRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := storePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := []byte(`{"name":"old","userId":"u1","external":{"keep":true}}`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Save(&StoredCompanion{Name: "new", UserID: "u2"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(after, &raw); err != nil {
		t.Fatal(err)
	}
	var external map[string]bool
	if err := json.Unmarshal(raw["external"], &external); err != nil {
		t.Fatal(err)
	}
	if !external["keep"] {
		t.Fatalf("external field not preserved: %s", raw["external"])
	}

	bad := []byte(`{"name":`)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(&StoredCompanion{Name: "next", UserID: "u3"}); err == nil {
		t.Fatal("Save should fail on invalid existing JSON")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(bad) {
		t.Fatalf("invalid buddy file was overwritten: %q", unchanged)
	}
}

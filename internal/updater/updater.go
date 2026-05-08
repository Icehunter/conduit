// Package updater checks GitHub Releases for newer versions of conduit
// and produces an install-method-aware upgrade hint.
//
// Conduit divergence from Claude Code: CC ships an embedded auto-updater
// tightly coupled to npm. Conduit is a single Go binary distributed via
// Homebrew/Scoop/winget plus direct GitHub Release downloads, so the
// update flow is a passive notifier with a hint, not a self-replacing
// downloader. Recorded in PARITY.md.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LatestReleaseURL is the GitHub API endpoint queried for the latest
// non-prerelease tag. Overridable in tests via [Checker.URL].
const LatestReleaseURL = "https://api.github.com/repos/Icehunter/conduit/releases/latest"

// CacheTTL is how long a successful check is honoured before re-querying.
const CacheTTL = 24 * time.Hour

// Result captures the outcome of a check.
type Result struct {
	Current    string    // e.g. "1.4.0"
	Latest     string    // e.g. "1.5.0" — empty when check failed or no newer release
	HasUpdate  bool      // true when Latest > Current
	CheckedAt  time.Time // wall-clock time the check completed
	UpgradeCmd string    // suggested command, e.g. "brew upgrade conduit"
}

// Checker queries GitHub Releases. Use [New] to construct one with sensible defaults.
type Checker struct {
	HTTPClient *http.Client
	URL        string
	CacheDir   string // when empty, caching is disabled
	Now        func() time.Time
}

// New returns a Checker with a 5 s HTTP timeout and caching under
// $HOME/.conduit/. If the home directory cannot be determined, caching
// is disabled (the check still works, it just won't be cached).
func New() *Checker {
	cacheDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		cacheDir = filepath.Join(home, ".conduit")
	}
	return &Checker{
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		URL:        LatestReleaseURL,
		CacheDir:   cacheDir,
		Now:        time.Now,
	}
}

// Check returns release info for current. The "v" prefix is tolerated
// on both sides. When current is "dev" or empty, HasUpdate is false
// (development builds opt out of the nag).
func (c *Checker) Check(ctx context.Context, current string) (Result, error) {
	now := c.Now()
	res := Result{Current: stripV(current), CheckedAt: now}

	if isDev(current) {
		return res, nil
	}

	if cached, ok := c.readCache(now); ok {
		res.Latest = cached.Latest
		res.HasUpdate = newer(cached.Latest, res.Current)
		res.UpgradeCmd = upgradeCmd(detectInstallMethod())
		return res, nil
	}

	latest, err := c.fetchLatest(ctx)
	if err != nil {
		return res, fmt.Errorf("updater: fetch: %w", err)
	}
	res.Latest = latest
	res.HasUpdate = newer(latest, res.Current)
	res.UpgradeCmd = upgradeCmd(detectInstallMethod())

	c.writeCache(cacheEntry{Latest: latest, CheckedAt: now})
	return res, nil
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

func (c *Checker) fetchLatest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "conduit-updater")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if rel.Draft || rel.Prerelease {
		return "", errors.New("latest release is draft or prerelease")
	}
	return stripV(rel.TagName), nil
}

type cacheEntry struct {
	Latest    string    `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
}

func (c *Checker) cachePath() string {
	if c.CacheDir == "" {
		return ""
	}
	return filepath.Join(c.CacheDir, "update-cache.json")
}

func (c *Checker) readCache(now time.Time) (cacheEntry, bool) {
	path := c.cachePath()
	if path == "" {
		return cacheEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cacheEntry{}, false
	}
	if now.Sub(entry.CheckedAt) > CacheTTL {
		return cacheEntry{}, false
	}
	return entry, true
}

func (c *Checker) writeCache(entry cacheEntry) {
	path := c.cachePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(c.CacheDir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	// Best-effort cache write; failure is non-fatal and surfaced only
	// via a re-fetch on the next check.
	_ = os.WriteFile(path, data, 0o644)
}

func stripV(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

func isDev(current string) bool {
	c := strings.TrimSpace(current)
	return c == "" || c == "dev" || strings.HasSuffix(c, "-dirty")
}

// newer reports whether latest > current using lexicographic semver
// comparison on the numeric components. Returns false on parse errors
// (we'd rather miss a notification than spam a wrong one).
func newer(latest, current string) bool {
	l, ok1 := parseSemver(latest)
	c, ok2 := parseSemver(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseSemver(s string) ([3]int, bool) {
	s = stripV(s)
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// Package update tells the user when a newer sitewarm release exists. It
// never installs anything and sends nothing but a plain GET for the latest
// release of the public repository.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultURL is the GitHub API endpoint for the latest stable release.
	DefaultURL = "https://api.github.com/repos/sitewarm/sitewarm/releases/latest"
	// ReleaseURL is where the notice points; the version is appended with a "v".
	ReleaseURL = "https://github.com/sitewarm/sitewarm/releases/tag/"

	ttl         = 24 * time.Hour
	fetchLimit  = 3 * time.Second
	maxBody     = 1 << 20
	cacheName   = "update-check.json"
	cacheSubdir = "sitewarm"
)

// Config is everything Start needs from the outside, so tests can fake it.
type Config struct {
	Current  string // running version, e.g. "1.0.2" or "dev"
	URL      string // latest-release endpoint
	CacheDir string // user cache dir; "" or an unwritable one disables the check
	Getenv   func(string) string
	TTY      bool // stderr is a terminal
	Disabled bool // --no-update-check
	Client   *http.Client
	Now      func() time.Time
}

// Check is a background look-up started with Start.
type Check struct {
	done   chan struct{}
	latest string
	cur    version
}

// Enabled reports whether a check should run at all: never for a dev build,
// when stderr is not a terminal, in CI, or when the user opted out.
func (c Config) Enabled() bool {
	if c.Disabled || !c.TTY || c.Getenv == nil {
		return false
	}
	if v := c.Getenv("SITEWARM_NO_UPDATE_CHECK"); v != "" && v != "0" {
		return false
	}
	if c.Getenv("CI") != "" || c.Getenv("GITHUB_ACTIONS") != "" {
		return false
	}
	_, ok := parse(c.Current)
	return ok
}

// Start looks up the latest release in the background and returns at once. It
// returns nil when the check is disabled.
func Start(ctx context.Context, c Config) *Check {
	if !c.Enabled() {
		return nil
	}
	cur, _ := parse(c.Current)
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Client == nil {
		c.Client = &http.Client{}
	}
	ck := &Check{done: make(chan struct{}), cur: cur}
	go func() {
		defer close(ck.done)
		ck.latest = latest(ctx, c)
	}()
	return ck
}

// Notice returns the one-line notice if a newer release is known, waiting at
// most wait for the look-up. It returns "" otherwise, and for a nil Check.
func (ck *Check) Notice(wait time.Duration) string {
	if ck == nil {
		return ""
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ck.done:
	case <-t.C:
		return ""
	}
	n, ok := parse(ck.latest)
	if !ok || !n.newerThan(ck.cur) {
		return ""
	}
	return fmt.Sprintf("sitewarm: %s is available (you have %s): %sv%s (silence this with SITEWARM_NO_UPDATE_CHECK=1)",
		n, ck.cur, ReleaseURL, n)
}

type cacheFile struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

// latest returns the newest known version: the cached one while it is
// fresh, otherwise a new look-up. Every failure yields "" or the stale value.
func latest(ctx context.Context, c Config) string {
	// Without a usable cache nothing could remember the answer or a failure,
	// so every run would spend one of the 60 unauthenticated requests an hour.
	if c.CacheDir == "" {
		return ""
	}
	path := filepath.Join(c.CacheDir, cacheSubdir, cacheName)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return ""
	}
	var cached cacheFile
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cached)
	}
	// A cached failure has an empty Latest; it still counts as fresh.
	if !cached.Checked.IsZero() && !cached.Checked.After(c.Now()) && c.Now().Sub(cached.Checked) < ttl {
		return cached.Latest
	}
	got, err := fetch(ctx, c)
	if err != nil {
		// Back off for a day on failure too, so an offline machine does not
		// retry on every run; keep the last good value.
		got = cached.Latest
	}
	writeCache(path, cacheFile{Checked: c.Now(), Latest: got})
	return got
}

func fetch(ctx context.Context, c Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "sitewarm/"+c.Current)
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update check: status %d", resp.StatusCode)
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&rel); err != nil {
		return "", err
	}
	v, ok := parse(rel.Tag)
	if !ok || rel.Draft || rel.Prerelease {
		return "", errors.New("update check: no usable release")
	}
	return v.String(), nil
}

// writeCache is best effort: a read-only home must not break a run.
func writeCache(path string, cf cacheFile) {
	b, err := json.Marshal(cf)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
	}
}

// version is a semantic version: major.minor.patch with an optional
// pre-release. Build metadata is ignored.
type version struct {
	n   [3]int
	pre string
}

func (v version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.n[0], v.n[1], v.n[2])
	if v.pre != "" {
		s += "-" + v.pre
	}
	return s
}

// parse accepts "1.2.3", "v1.2.3" and "1.2.3-rc.1" and rejects anything
// else, so a hostile tag name never reaches the terminal.
func parse(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || len(p) > 9 || (len(p) > 1 && p[0] == '0') {
			return version{}, false
		}
		v.n[i] = n
	}
	for _, r := range pre {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-') {
			return version{}, false
		}
	}
	if strings.Contains(s, "-") && pre == "" {
		return version{}, false
	}
	v.pre = pre
	return v, true
}

// newerThan orders by number, then a release above its own pre-release.
// Two different pre-releases of one version compare as not newer: the
// notice only ever points at stable releases.
func (v version) newerThan(o version) bool {
	for i := range v.n {
		if v.n[i] != o.n[i] {
			return v.n[i] > o.n[i]
		}
	}
	return v.pre == "" && o.pre != ""
}

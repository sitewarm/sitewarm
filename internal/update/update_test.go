package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fake struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func newFake(t *testing.T, status int, body string) *fake {
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected request: %s auth=%q", r.Method, r.Header.Get("Authorization"))
		}
		if ua := r.Header.Get("User-Agent"); ua != "sitewarm/1.0.0" {
			t.Errorf("User-Agent = %q", ua)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func cfg(t *testing.T, url string) Config {
	return Config{
		Current: "1.0.0", URL: url, CacheDir: t.TempDir(), TTY: true,
		Getenv: func(string) string { return "" },
		Now:    func() time.Time { return t0 },
	}
}

func notice(t *testing.T, c Config) string {
	t.Helper()
	return Start(context.Background(), c).Notice(5 * time.Second)
}

func TestNoticeWhenNewer(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v1.1.0"}`)
	got := notice(t, cfg(t, f.srv.URL))
	for _, want := range []string{"1.1.0 is available", "you have 1.0.0", "releases/tag/v1.1.0", "SITEWARM_NO_UPDATE_CHECK=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "\n") {
		t.Errorf("notice must be one line: %q", got)
	}
}

func TestNoNoticeWhenCurrentOrOlder(t *testing.T) {
	for _, tag := range []string{"v1.0.0", "v0.9.9"} {
		f := newFake(t, 200, `{"tag_name":"`+tag+`"}`)
		if got := notice(t, cfg(t, f.srv.URL)); got != "" {
			t.Errorf("tag %s: unexpected notice %q", tag, got)
		}
	}
}

func TestPrereleaseBuildSeesStableRelease(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v1.0.0"}`)
	c := cfg(t, f.srv.URL)
	c.Current = "1.0.0-rc.1"
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	})
	if got := notice(t, c); !strings.Contains(got, "1.0.0 is available") {
		t.Errorf("rc build should be told about 1.0.0, got %q", got)
	}
}

func TestSilentCases(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v9.0.0"}`)
	cases := map[string]func(*Config){
		"opt-out env":   func(c *Config) { c.Getenv = env("SITEWARM_NO_UPDATE_CHECK", "1") },
		"opt-out flag":  func(c *Config) { c.Disabled = true },
		"CI":            func(c *Config) { c.Getenv = env("CI", "true") },
		"GitHub Action": func(c *Config) { c.Getenv = env("GITHUB_ACTIONS", "true") },
		"not a tty":     func(c *Config) { c.TTY = false },
		"dev build":     func(c *Config) { c.Current = "dev" },
		"garbage build": func(c *Config) { c.Current = "not-a-version" },
	}
	for name, mod := range cases {
		c := cfg(t, f.srv.URL)
		mod(&c)
		if got := notice(t, c); got != "" {
			t.Errorf("%s: unexpected notice %q", name, got)
		}
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("disabled checks made %d requests, want 0", n)
	}
}

func TestOptOutZeroDoesNotDisable(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v1.1.0"}`)
	c := cfg(t, f.srv.URL)
	c.Getenv = env("SITEWARM_NO_UPDATE_CHECK", "0")
	if notice(t, c) == "" {
		t.Error("SITEWARM_NO_UPDATE_CHECK=0 should leave the check on")
	}
}

func env(k, v string) func(string) string {
	return func(s string) string {
		if s == k {
			return v
		}
		return ""
	}
}

func TestCacheWithinTTLSkipsNetwork(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v1.1.0"}`)
	c := cfg(t, f.srv.URL)
	notice(t, c)
	if got := notice(t, c); !strings.Contains(got, "1.1.0") {
		t.Errorf("cached notice missing: %q", got)
	}
	if n := f.hits.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (second run served from cache)", n)
	}
}

func TestCacheExpiresAfterTTL(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v1.1.0"}`)
	c := cfg(t, f.srv.URL)
	notice(t, c)
	c.Now = func() time.Time { return t0.Add(25 * time.Hour) }
	notice(t, c)
	if n := f.hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 after the TTL", n)
	}
}

func TestOfflineIsSilentAndBacksOff(t *testing.T) {
	f := newFake(t, 500, `oops`)
	c := cfg(t, f.srv.URL)
	if got := notice(t, c); got != "" {
		t.Errorf("failed run printed %q", got)
	}
	// The failure is cached too, so the next run does not retry within the TTL.
	if got := notice(t, c); got != "" {
		t.Errorf("second failed run printed %q", got)
	}
	if n := f.hits.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (the second run backs off)", n)
	}
	c.Now = func() time.Time { return t0.Add(25 * time.Hour) }
	notice(t, c)
	if n := f.hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 once the backoff has expired", n)
	}
}

func TestNoUsableCacheMakesNoRequest(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v1.1.0"}`)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"no cache dir":       "",
		"uncreatable folder": filepath.Join(file, "sub"),
	} {
		c := cfg(t, f.srv.URL)
		c.CacheDir = dir
		for i := 0; i < 3; i++ {
			if got := notice(t, c); got != "" {
				t.Errorf("%s: unexpected notice %q", name, got)
			}
		}
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("requests = %d, want 0 without a usable cache", n)
	}
}

func TestFailureKeepsLastGoodValue(t *testing.T) {
	good := newFake(t, 200, `{"tag_name":"v1.1.0"}`)
	c := cfg(t, good.srv.URL)
	notice(t, c)
	bad := newFake(t, 500, `oops`)
	c.URL = bad.srv.URL
	c.Now = func() time.Time { return t0.Add(48 * time.Hour) }
	if got := notice(t, c); !strings.Contains(got, "1.1.0") {
		t.Errorf("stale value lost after a failed refresh: %q", got)
	}
}

func TestBadResponsesAreIgnored(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `<html>`,
		"hostile":    `{"tag_name":"v9.9.9\u001b]0;pwned\u0007"}`,
		"prerelease": `{"tag_name":"v2.0.0-rc.1","prerelease":true}`,
		"draft":      `{"tag_name":"v2.0.0","draft":true}`,
		"empty":      `{}`,
	} {
		f := newFake(t, 200, body)
		if got := notice(t, cfg(t, f.srv.URL)); got != "" {
			t.Errorf("%s: unexpected notice %q", name, got)
		}
	}
}

func TestSlowServerDoesNotDelayNotice(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	ck := Start(ctx, cfg(t, srv.URL))
	got := ck.Notice(50 * time.Millisecond)
	cancel()
	<-ck.done // the look-up ends with the context, before TempDir cleanup
	if got != "" || time.Since(start) > time.Second {
		t.Errorf("slow server: notice %q after %v", got, time.Since(start))
	}
}

func TestNilCheckIsSafe(t *testing.T) {
	var ck *Check
	if ck.Notice(time.Second) != "" {
		t.Error("nil Check returned a notice")
	}
}

func TestCacheFilePermissions(t *testing.T) {
	f := newFake(t, 200, `{"tag_name":"v1.1.0"}`)
	c := cfg(t, f.srv.URL)
	notice(t, c)
	fi, err := os.Stat(filepath.Join(c.CacheDir, "sitewarm", cacheName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return // no Unix permission bits on Windows; the cache file is still written (Stat succeeded)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestParseAndCompare(t *testing.T) {
	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "1.x.3", "01.2.3", "1.2.3-", "1.2.3-a b", "-1.2.3", "dev"} {
		if _, ok := parse(bad); ok {
			t.Errorf("parse(%q) accepted", bad)
		}
	}
	older := [][2]string{{"1.0.1", "1.0.0"}, {"1.10.0", "1.9.9"}, {"2.0.0", "1.99.99"}, {"1.0.0", "1.0.0-rc.1"}}
	for _, p := range older {
		a, _ := parse(p[0])
		b, _ := parse(p[1])
		if !a.newerThan(b) || b.newerThan(a) {
			t.Errorf("%s should be newer than %s", p[0], p[1])
		}
	}
	a, _ := parse("v1.0.0+build5")
	b, _ := parse("1.0.0")
	if a.newerThan(b) || b.newerThan(a) {
		t.Error("build metadata must not order versions")
	}
}

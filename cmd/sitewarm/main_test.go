package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sitewarm/sitewarm/internal/update"
	"github.com/sitewarm/sitewarm/pkg/sitewarm"
)

func server(cacheStatus string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cf-Cache-Status", cacheStatus)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><head></head></html>"))
	}))
}

func csvFor(t *testing.T, urls ...string) string {
	path := filepath.Join(t.TempDir(), "top.csv")
	body := "url\n"
	for _, u := range urls {
		body += u + "\n"
	}
	os.WriteFile(path, []byte(body), 0o600)
	return path
}

type harness struct {
	stdout, stderr bytes.Buffer
	signals        chan os.Signal
	mu             sync.Mutex
	exits          []int
}

func newHarness() *harness { return &harness{signals: make(chan os.Signal, 2)} }

func (h *harness) env() env {
	return env{
		stdout: &h.stdout, stderr: &h.stderr, getenv: func(string) string { return "" },
		signals: h.signals,
		exit: func(code int) {
			h.mu.Lock()
			h.exits = append(h.exits, code)
			h.mu.Unlock()
		},
	}
}

func readReport(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func slowScanArgs(t *testing.T, srvURL string) (args []string, reportPath string) {
	reportPath = filepath.Join(t.TempDir(), "report.json")
	return []string{"scan", "--top-from", csvFor(t, srvURL+"/a", srvURL+"/b"),
		"--rps", "0", "--retry-interval", "20ms", "--max-wait", "1m",
		"--report", reportPath, "--format", "json"}, reportPath
}

func TestSignalWritesPartialReport(t *testing.T) {
	for _, tc := range []struct {
		sig  os.Signal
		code int
		name string
	}{
		{syscall.SIGINT, 130, "SIGINT"},
		{syscall.SIGTERM, 143, "SIGTERM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := server("MISS")
			defer srv.Close()
			h := newHarness()
			args, reportPath := slowScanArgs(t, srv.URL)
			go func() {
				time.Sleep(200 * time.Millisecond)
				h.signals <- tc.sig
			}()
			code := run(context.Background(), args, h.env())
			if code != tc.code {
				t.Fatalf("exit %d, want %d; stderr: %s", code, tc.code, h.stderr.String())
			}
			r := readReport(t, reportPath)
			if r["stopped"] != tc.name {
				t.Fatalf("stopped = %v", r["stopped"])
			}
			results := r["results"].([]any)
			if len(results) != 4 {
				t.Fatalf("partial report has %d results, want all 4", len(results))
			}
			for _, e := range results {
				if s := e.(map[string]any)["state"]; s != "incomplete" {
					t.Fatalf("state %v, want incomplete", s)
				}
			}
			if h.stdout.Len() == 0 {
				t.Fatal("report not printed")
			}
		})
	}
}

func TestSecondSignalForcesExit(t *testing.T) {
	srv := server("MISS")
	defer srv.Close()
	h := newHarness()
	args, _ := slowScanArgs(t, srv.URL)
	h.signals <- syscall.SIGINT
	h.signals <- syscall.SIGINT
	run(context.Background(), args, h.env())
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.exits) != 1 || h.exits[0] != 130 {
		t.Fatalf("forced exits = %v, want [130]", h.exits)
	}
}

func TestExitCodes(t *testing.T) {
	hit := server("HIT")
	defer hit.Close()
	miss := server("MISS")
	defer miss.Close()

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"pass", []string{"scan", "--top-from", csvFor(t, hit.URL+"/"), "--rps", "0"}, 0},
		{"fail-on miss", []string{"scan", "--top-from", csvFor(t, miss.URL+"/"), "--rps", "0", "--max-wait", "0"}, 1},
		{"empty fail-on", []string{"scan", "--top-from", csvFor(t, miss.URL+"/"), "--rps", "0", "--max-wait", "0", "--fail-on", ""}, 0},
		{"no source", []string{"scan"}, 2},
		{"bad flag", []string{"scan", "--nope"}, 2},
		{"bad fail-on", []string{"scan", "--top-from", csvFor(t, hit.URL+"/"), "--fail-on", "nope"}, 2},
		{"bad format", []string{"scan", "--top-from", csvFor(t, hit.URL+"/"), "--format", "xml"}, 2},
		{"two sources", []string{"scan", "--top-from", csvFor(t, hit.URL+"/"), "--from-sitemap", hit.URL + "/sitemap.xml"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness()
			if got := run(context.Background(), c.args, h.env()); got != c.want {
				t.Fatalf("exit %d, want %d; stderr: %s", got, c.want, h.stderr.String())
			}
		})
	}
}

func TestCookieWithoutCookieHostsFailsAtStart(t *testing.T) {
	srv := server("HIT")
	defer srv.Close()
	h := newHarness()
	e := h.env()
	e.getenv = func(k string) string {
		if k == "SITEWARM_COOKIE_DESKTOP" {
			return "session=abc"
		}
		return ""
	}
	if code := run(context.Background(), []string{"scan", "--top-from", csvFor(t, srv.URL+"/")}, e); code != 2 {
		t.Fatalf("exit %d, want 2; stderr: %s", code, h.stderr.String())
	}
}

func TestOtherCommandsMakeNoUpdateRequest(t *testing.T) {
	old := sitewarm.Version
	sitewarm.Version = "1.0.0"
	t.Cleanup(func() { sitewarm.Version = old })
	var hits atomic.Int32
	rel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"tag_name":"v1.1.0"}`))
	}))
	defer rel.Close()

	for _, args := range [][]string{{"--version"}, {"--help"}, {"help"}, {"scan", "--help"}, {"completion", "bash"}} {
		h := newHarness()
		e := h.env()
		e.update = update.Config{URL: rel.URL, CacheDir: t.TempDir(), TTY: true}
		run(context.Background(), args, e)
		if strings.Contains(h.stderr.String(), "available") {
			t.Errorf("%v: printed an update notice: %q", args, h.stderr.String())
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("update requests = %d, want 0 outside scan", n)
	}
}

func TestUpdateNotice(t *testing.T) {
	old := sitewarm.Version
	sitewarm.Version = "1.0.0"
	t.Cleanup(func() { sitewarm.Version = old })
	rel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.1.0"}`))
	}))
	defer rel.Close()
	site := server("HIT")
	defer site.Close()

	for name, tc := range map[string]struct {
		extra []string
		want  bool
	}{"on a terminal": {nil, true}, "opted out by flag": {[]string{"--no-update-check"}, false}} {
		h := newHarness()
		e := h.env()
		e.update = update.Config{URL: rel.URL, CacheDir: t.TempDir(), TTY: true}
		args := append([]string{"scan", "--top-from", csvFor(t, site.URL+"/"), "--max-wait", "0", "--format", "csv"}, tc.extra...)
		run(context.Background(), args, e)
		if got := strings.Contains(h.stderr.String(), "1.1.0 is available"); got != tc.want {
			t.Errorf("%s: notice shown = %v, want %v; stderr=%q", name, got, tc.want, h.stderr.String())
		}
		if strings.Contains(h.stdout.String(), "available") {
			t.Errorf("%s: notice leaked to stdout", name)
		}
	}
}

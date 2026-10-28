package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRobotsAllowedRejectsAnUnparsableURL(t *testing.T) {
	ok, err := Fetcher{}.NewRobots().Allowed(context.Background(), "http://%zz/")
	if err == nil || ok {
		t.Fatalf("Allowed = %v, %v; want false and an error", ok, err)
	}
}

// A lookup cut short by the context says nothing about the host: it returns
// the context error, records no warning and caches nothing, so a later lookup
// asks the host again.
func TestRobotsCanceledLookupIsNotCachedAndNotAWarning(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	}))
	defer srv.Close()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	ok, err := robots.Allowed(canceled, srv.URL+"/page")
	if ok || err != context.Canceled {
		t.Fatalf("Allowed = %v, %v; want false, context canceled", ok, err)
	}
	if w := robots.Warnings(); len(w) != 0 {
		t.Fatalf("warnings %v after a canceled lookup", w)
	}
	if ok, err = robots.Allowed(context.Background(), srv.URL+"/private"); err != nil || ok {
		t.Fatalf("later lookup = %v, %v; want the rules to apply", ok, err)
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("%d requests, want 1 (the canceled lookup sent none)", n)
	}
}

// Same, when the cancel arrives while the body is being read.
func TestRobotsCanceledWhileReadingTheBody(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()
	ok, err := robots.Allowed(ctx, srv.URL+"/page")
	if ok || err != context.Canceled {
		t.Fatalf("Allowed = %v, %v; want false, context canceled", ok, err)
	}
	if w := robots.Warnings(); len(w) != 0 {
		t.Fatalf("warnings %v", w)
	}
}

// A body that breaks off with the context still alive is an unavailable
// robots.txt: the host is skipped with a warning, not allowed.
func TestRobotsBodyCutShortSkipsTheHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("User-agent: *\n"))
	}))
	defer srv.Close()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()
	for range 2 {
		ok, err := robots.Allowed(context.Background(), srv.URL+"/page")
		if err != nil || ok {
			t.Fatalf("Allowed = %v, %v; want false, nil", ok, err)
		}
	}
	w := robots.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "could not be read") || !strings.Contains(w[0], "skipped-robots") {
		t.Fatalf("warnings %v, want one 'could not be read ... skipped-robots'", w)
	}
}

// Two first lookups of one host that overlap both fetch robots.txt; they must
// agree, and the next lookup must use the stored entry.
func TestRobotsConcurrentFirstLookupsOfOneHostAgree(t *testing.T) {
	var arrived atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		w.Write([]byte("User-agent: sitewarm\nDisallow: /private\n"))
	}))
	defer srv.Close()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = robots.Allowed(context.Background(), srv.URL+"/private/x")
		}()
	}
	wg.Wait()
	if n := arrived.Load(); n != 2 {
		t.Fatalf("%d requests; the test needs both first lookups to fetch", n)
	}
	for i := range results {
		if errs[i] != nil || results[i] {
			t.Fatalf("lookup %d = %v, %v; want disallowed", i, results[i], errs[i])
		}
	}
	if ok, err := robots.Allowed(context.Background(), srv.URL+"/public"); err != nil || !ok {
		t.Fatalf("later lookup = %v, %v", ok, err)
	}
	if n := arrived.Load(); n != 2 {
		t.Fatalf("%d requests after the later lookup, want it served from the stored entry", n)
	}
}

// A URL with no path is the root: "/" for robots purposes.
func TestRobotsEmptyPathIsTheRoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /$\n"))
	}))
	defer srv.Close()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()
	for url, want := range map[string]bool{srv.URL: false, srv.URL + "/": false, srv.URL + "/x": true} {
		ok, err := robots.Allowed(context.Background(), url)
		if err != nil || ok != want {
			t.Errorf("Allowed(%q) = %v, %v; want %v", url, ok, err, want)
		}
	}
}

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

// Lookups of one host that overlap share a single robots.txt request: one
// request, one warning, and every caller gets the same answer.
func overlappingLookups(t *testing.T, status int) (requests int, robots *Robots, allowed []bool) {
	t.Helper()
	const callers = 16
	var hits atomic.Int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte("User-agent: *\nDisallow: /private\n"))
		}
	}))
	defer srv.Close()
	robots = Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()

	var started, done sync.WaitGroup
	allowed = make([]bool, callers)
	errs := make([]error, callers)
	for i := range callers {
		started.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			started.Done()
			allowed[i], errs[i] = robots.Allowed(context.Background(), srv.URL+"/private/x")
		}()
	}
	started.Wait()
	time.Sleep(100 * time.Millisecond) // let every caller reach the lookup while the request is held
	close(gate)
	done.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	return int(hits.Load()), robots, allowed
}

func TestRobotsOverlappingLookupsShareOneRequestAndOneWarning(t *testing.T) {
	requests, robots, allowed := overlappingLookups(t, http.StatusServiceUnavailable)
	if requests != 1 {
		t.Fatalf("%d robots.txt requests, want 1", requests)
	}
	if w := robots.Warnings(); len(w) != 1 {
		t.Fatalf("%d warnings %v, want 1", len(w), w)
	}
	for i, ok := range allowed {
		if ok {
			t.Fatalf("caller %d allowed on a host whose robots.txt is unavailable", i)
		}
	}
}

func TestRobotsOverlappingLookupsAgreeOnTheRules(t *testing.T) {
	requests, robots, allowed := overlappingLookups(t, http.StatusOK)
	if requests != 1 {
		t.Fatalf("%d robots.txt requests, want 1", requests)
	}
	for i, ok := range allowed {
		if ok {
			t.Fatalf("caller %d allowed /private/x, want disallowed", i)
		}
	}
	if w := robots.Warnings(); len(w) != 0 {
		t.Fatalf("warnings %v", w)
	}
}

// A caller that gives up while another lookup is loading returns its own
// context error and leaves the load running for the others.
func TestRobotsWaiterCanceledWhileAnotherLookupLoads(t *testing.T) {
	var hits atomic.Int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate
		w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	}))
	defer srv.Close()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()

	leader := make(chan bool, 1)
	go func() {
		ok, _ := robots.Allowed(context.Background(), srv.URL+"/private/x")
		leader <- ok
	}()
	for hits.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if ok, err := robots.Allowed(ctx, srv.URL+"/other"); ok || err != context.DeadlineExceeded {
		t.Fatalf("waiter = %v, %v; want false, deadline exceeded", ok, err)
	}
	close(gate)
	if ok := <-leader; ok {
		t.Fatal("leader allowed /private/x")
	}
	if ok, err := robots.Allowed(context.Background(), srv.URL+"/other"); err != nil || !ok {
		t.Fatalf("later lookup = %v, %v", ok, err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

// If the lookup that is loading is canceled, a waiter does not inherit the
// cancellation: it loads robots.txt itself.
func TestRobotsWaiterTakesOverWhenTheLoadingLookupIsCanceled(t *testing.T) {
	var hits atomic.Int32
	first := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			once.Do(func() { close(first) })
			<-r.Context().Done() // the first request hangs until its caller gives up
			return
		}
		w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	}))
	defer srv.Close()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := robots.Allowed(leaderCtx, srv.URL+"/private/x")
		leaderDone <- err
	}()
	<-first
	waiter := make(chan bool, 1)
	waiterErr := make(chan error, 1)
	go func() {
		ok, err := robots.Allowed(context.Background(), srv.URL+"/private/x")
		waiter <- ok
		waiterErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancelLeader()
	if err := <-leaderDone; err != context.Canceled {
		t.Fatalf("leader = %v, want context canceled", err)
	}
	if ok, err := <-waiter, <-waiterErr; err != nil || ok {
		t.Fatalf("waiter = %v, %v; want it to load the rules and find /private/x disallowed", ok, err)
	}
	if w := robots.Warnings(); len(w) != 0 {
		t.Fatalf("warnings %v after a canceled load", w)
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

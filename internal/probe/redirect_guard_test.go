package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// locationServer answers every request with status and, when location is not
// nil, that Location header. It counts the requests it receives.
func locationServer(t *testing.T, status int, location *string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if location != nil {
			w.Header()["Location"] = []string{*location}
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A redirect must never leave http and https: the engine does not follow it,
// reports it, and sends no further request.
func TestRedirectToUnsupportedSchemeIsReportedNotFollowed(t *testing.T) {
	for _, tc := range []struct{ location, scheme string }{
		{"ftp://example.com/file", "ftp"},
		{"javascript:alert(1)", "javascript"},
		{"file:///etc/passwd", "file"},
		{"mailto:someone@example.com", "mailto"},
		{"data:text/html,hello", "data"},
	} {
		t.Run(tc.scheme, func(t *testing.T) {
			loc := tc.location
			srv, hits := locationServer(t, http.StatusFound, &loc)
			obs := newProber(t, Config{}).Do(context.Background(), srv.URL+"/", desktop)
			if want := "redirect to unsupported scheme " + tc.scheme; obs.RedirectError != want {
				t.Fatalf("RedirectError = %q, want %q", obs.RedirectError, want)
			}
			if obs.Err != nil || obs.Status != http.StatusFound {
				t.Fatalf("Err %v, status %d: the 302 itself is a valid answer", obs.Err, obs.Status)
			}
			if len(obs.Redirects) != 1 || obs.Redirects[0].Status != http.StatusFound {
				t.Fatalf("redirects = %v, want the one hop that was taken", obs.Redirects)
			}
			if n := hits.Load(); n != 1 {
				t.Fatalf("%d requests, want 1: the target must not be requested", n)
			}
		})
	}
}

// http to https on the same host is the common case and must still work.
func TestRedirectSchemeChecksAllowHTTPAndHTTPSOnly(t *testing.T) {
	srv := redirectServer(t, map[string]string{"/a": "/b"})
	defer srv.Close()
	obs := newProber(t, Config{}).Do(context.Background(), srv.URL+"/a", desktop)
	if obs.RedirectError != "" || obs.Status != http.StatusOK {
		t.Fatalf("error %q status %d", obs.RedirectError, obs.Status)
	}
}

func TestRedirectWithoutLocation(t *testing.T) {
	empty := ""
	for _, status := range []int{301, 302, 303, 307, 308} {
		for name, location := range map[string]*string{"absent": nil, "empty": &empty} {
			t.Run(fmt.Sprintf("%d %s", status, name), func(t *testing.T) {
				srv, hits := locationServer(t, status, location)
				obs := newProber(t, Config{}).Do(context.Background(), srv.URL+"/", desktop)
				if want := fmt.Sprintf("%d without Location", status); obs.RedirectError != want {
					t.Fatalf("RedirectError = %q, want %q", obs.RedirectError, want)
				}
				if obs.Err != nil || obs.Status != status || len(obs.Redirects) != 1 {
					t.Fatalf("Err %v, status %d, redirects %v", obs.Err, obs.Status, obs.Redirects)
				}
				if n := hits.Load(); n != 1 {
					t.Fatalf("%d requests, want 1", n)
				}
			})
		}
	}
}

// An unparsable Location is never followed and costs one request. net/http
// rejects it before the client's CheckRedirect runs, so it reaches Do as a
// request error and not as a RedirectError: the "invalid Location" branch in
// Do cannot be reached through a real server. This test pins what happens
// today (a request error that names the Location), whichever of the two paths
// reports it.
func TestRedirectWithUnparsableLocation(t *testing.T) {
	for _, loc := range []string{"http://%zz/", "http://[::1", "http://exa mple.com/"} {
		t.Run(loc, func(t *testing.T) {
			loc := loc
			srv, hits := locationServer(t, http.StatusMovedPermanently, &loc)
			obs := newProber(t, Config{}).Do(context.Background(), srv.URL+"/", desktop)
			reported := strings.HasPrefix(obs.RedirectError, "invalid Location: ") ||
				(obs.Err != nil && strings.Contains(obs.Err.Error(), "Location"))
			if !reported {
				t.Fatalf("Err %v, RedirectError %q: the bad Location is not reported", obs.Err, obs.RedirectError)
			}
			if hits.Load() != 1 {
				t.Fatalf("%d requests, want 1: the target must not be requested", hits.Load())
			}
		})
	}
}

// 300, 304 and 305 are not redirects Do follows: they come back as the final
// answer, with no redirect error (verify.Classify decides what they mean).
func TestOnlyFiveStatusesAreFollowedAsRedirects(t *testing.T) {
	for status, want := range map[int]bool{
		200: false, 299: false, 300: false, 301: true, 302: true, 303: true, 304: false,
		305: false, 306: false, 307: true, 308: true, 309: false, 400: false,
	} {
		if got := isRedirect(status); got != want {
			t.Errorf("isRedirect(%d) = %v, want %v", status, got, want)
		}
	}
	loc := "/elsewhere"
	srv, hits := locationServer(t, http.StatusMultipleChoices, &loc)
	obs := newProber(t, Config{}).Do(context.Background(), srv.URL+"/", desktop)
	if obs.Status != http.StatusMultipleChoices || obs.RedirectError != "" || len(obs.Redirects) != 0 || hits.Load() != 1 {
		t.Fatalf("status %d, error %q, redirects %v, requests %d", obs.Status, obs.RedirectError, obs.Redirects, hits.Load())
	}
}

func TestDoWithUnparsableURLReturnsErrorWithoutRequest(t *testing.T) {
	obs := newProber(t, Config{}).Do(context.Background(), "http://%zz/", desktop)
	if obs.Err == nil || obs.Status != 0 || obs.URL != "http://%zz/" {
		t.Fatalf("Err %v, status %d, URL %q", obs.Err, obs.Status, obs.URL)
	}
}

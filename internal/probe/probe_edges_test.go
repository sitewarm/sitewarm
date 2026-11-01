package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Only the meta names "robots" and "googlebot" count; noindex under any other
// name (another crawler, or no name at all) leaves the page indexable.
func TestMetaNameScope(t *testing.T) {
	for _, tc := range []struct {
		meta string
		want Indexability
	}{
		{`<meta name="robots" content="noindex">`, NoIndex},
		{`<meta name="googlebot" content="noindex">`, NoIndex},
		{`<meta name=" GoogleBot " content="none">`, NoIndex},
		{`<meta name="bingbot" content="noindex">`, Indexable},
		{`<meta name="description" content="noindex">`, Indexable},
		{`<meta property="robots" content="noindex">`, Indexable},
		{`<meta content="noindex">`, Indexable},
		{`<meta name="robots" content="index, follow">`, Indexable},
	} {
		got, _, _ := scanHead([]byte("<html><head>" + tc.meta + "</head><body></body></html>"))
		if got != tc.want {
			t.Errorf("%s: indexability = %s, want %s", tc.meta, got, tc.want)
		}
	}
}

// The body is analysed for 2xx only: 299 is read, 300 is not.
func TestBodyAnalysedFor2xxOnly(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   Indexability
	}{
		{200, NoIndex},
		{299, NoIndex},
		{300, ""},
		{404, ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(tc.status)
			w.Write([]byte(noindexPage))
		}))
		obs := newProber(t, Config{}).Do(context.Background(), srv.URL, desktop)
		srv.Close()
		if obs.Err != nil {
			t.Fatalf("status %d: %v", tc.status, obs.Err)
		}
		if obs.Indexability != tc.want {
			t.Errorf("status %d: indexability = %q, want %q", tc.status, obs.Indexability, tc.want)
		}
	}
}

// A canonical URL of exactly maxCanonical bytes is kept; one byte more is
// dropped.
func TestResolveCanonicalLengthBoundary(t *testing.T) {
	page, _ := url.Parse("https://example.com/p")
	prefix := "https://example.com/"
	for _, tc := range []struct {
		n    int
		keep bool
	}{
		{maxCanonical - 1, true},
		{maxCanonical, true},
		{maxCanonical + 1, false},
	} {
		href := prefix + strings.Repeat("a", tc.n-len(prefix))
		got := resolveCanonical(page, href)
		if tc.keep && got != href {
			t.Errorf("length %d: got %d bytes, want it kept", tc.n, len(got))
		}
		if !tc.keep && got != "" {
			t.Errorf("length %d: kept, want dropped", tc.n)
		}
	}
}

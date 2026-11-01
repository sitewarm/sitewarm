package source

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// writeSitemapOfSize writes a valid one-URL sitemap of exactly total bytes:
// whitespace after the root element is valid XML, so the padding needs no
// bytes held in memory.
func writeSitemapOfSize(w io.Writer, total int) {
	const doc = `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://example.com/a</loc></url></urlset>`
	io.WriteString(w, doc)
	pad := bytes.Repeat([]byte{' '}, 64<<10)
	for left := total - len(doc); left > 0; {
		n := min(left, len(pad))
		w.Write(pad[:n])
		left -= n
	}
}

func sizedSitemapServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var total int
		switch strings.TrimSuffix(r.URL.Path, ".gz") {
		case "/exact":
			total = MaxSitemapBytes
		case "/over":
			total = MaxSitemapBytes + 1
		default:
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".gz") {
			// A few dozen KB on the wire, 50 MB once inflated.
			gz := gzip.NewWriter(w)
			writeSitemapOfSize(gz, total)
			gz.Close()
			return
		}
		writeSitemapOfSize(w, total)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The 50 MB limit of the sitemaps.org protocol counts uncompressed bytes: a
// small gzip file must not inflate past it, and a file of exactly the limit
// is still read.
func TestReadSitemapSizeLimitIsOnUncompressedBytes(t *testing.T) {
	srv := sizedSitemapServer(t)
	f := Fetcher{Client: srv.Client(), UserAgent: "test"}
	for _, path := range []string{"/exact", "/exact.gz"} {
		urls, err := f.ReadSitemap(context.Background(), srv.URL+path, 0, nil)
		if err != nil || len(urls) != 1 || urls[0] != "https://example.com/a" {
			t.Errorf("%s (exactly %d bytes): urls %v, err %v", path, MaxSitemapBytes, urls, err)
		}
	}
	for _, path := range []string{"/over", "/over.gz"} {
		urls, err := f.ReadSitemap(context.Background(), srv.URL+path, 0, nil)
		if err == nil || !strings.Contains(err.Error(), "larger than 50 MB uncompressed") {
			t.Errorf("%s (%d bytes): err %v, want the size limit", path, MaxSitemapBytes+1, err)
		}
		if len(urls) != 0 {
			t.Errorf("%s: %d URLs returned from an oversized sitemap", path, len(urls))
		}
		if err != nil && !strings.Contains(err.Error(), srv.URL+path) {
			t.Errorf("%s: error %q does not name the sitemap", path, err)
		}
	}
}

func TestReadSitemapIndexWithRelativeChildFails(t *testing.T) {
	s := newSite(t, map[string]string{"/index.xml": index("/child.xml")})
	_, err := fetcher(s).ReadSitemap(context.Background(), s.srv.URL+"/index.xml", 0, nil)
	if err == nil || !strings.Contains(err.Error(), "not an absolute") {
		t.Fatalf("got %v, want a relative child to be refused", err)
	}
	if n := s.count("/child.xml"); n != 0 {
		t.Fatalf("the relative child was requested %d times", n)
	}
}

func TestReadSitemapWaitErrorStopsBeforeTheRequest(t *testing.T) {
	s := newSite(t, map[string]string{"/sitemap.xml": urlset("https://example.com/")})
	stop := errors.New("run is over")
	f := Fetcher{Client: s.srv.Client(), UserAgent: "test", Wait: func(context.Context) error { return stop }}
	_, err := f.ReadSitemap(context.Background(), s.srv.URL+"/sitemap.xml", 0, nil)
	if !errors.Is(err, stop) {
		t.Fatalf("got %v, want the Wait error", err)
	}
	if n := s.count("/sitemap.xml"); n != 0 {
		t.Fatalf("%d requests after Wait failed, want none", n)
	}
}

func TestFetcherWithoutClientUsesTheDefaultClient(t *testing.T) {
	s := newSite(t, map[string]string{"/sitemap.xml": urlset("https://example.com/")})
	urls, err := Fetcher{UserAgent: "test"}.ReadSitemap(context.Background(), s.srv.URL+"/sitemap.xml", 0, nil)
	if err != nil || len(urls) != 1 {
		t.Fatalf("urls %v, err %v", urls, err)
	}
}

func TestReadSitemapUnreachableServerNamesTheSitemap(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/sitemap.xml"
	srv.Close() // connection refused
	urls, err := Fetcher{UserAgent: "test"}.ReadSitemap(context.Background(), url, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "sitemap "+url) || errors.Is(err, ErrEmpty) || len(urls) != 0 {
		t.Fatalf("urls %v, err %v", urls, err)
	}
}

// Content that claims to be gzip (magic bytes) but is not, or is cut short,
// is an error that names the sitemap, never an empty or partial result.
func TestReadSitemapBrokenGzip(t *testing.T) {
	full := gzipBytes(t, urlset("https://example.com/a", "https://example.com/b"))
	for name, body := range map[string][]byte{
		"magic bytes only":    {0x1f, 0x8b},
		"bad header":          append([]byte{0x1f, 0x8b, 0xff, 0xff}, bytes.Repeat([]byte{0}, 20)...),
		"truncated stream":    full[:len(full)/2],
		"truncated at header": full[:12],
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
			defer srv.Close()
			urls, err := Fetcher{Client: srv.Client(), UserAgent: "test"}.ReadSitemap(context.Background(), srv.URL, 0, nil)
			if err == nil || errors.Is(err, ErrEmpty) {
				t.Fatalf("urls %v, err %v: want a decoding error", urls, err)
			}
			if !strings.Contains(err.Error(), "sitemap "+srv.URL) {
				t.Fatalf("error %q does not name the sitemap", err)
			}
		})
	}
}

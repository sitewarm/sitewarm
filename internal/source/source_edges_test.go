package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// When a CSV has both columns, "url" wins wherever it sits, and no --base-url
// is needed.
func TestReadCSVURLColumnWinsOverPathColumn(t *testing.T) {
	for _, in := range []string{
		"url,path\nhttps://ex.com/a,/ignored\n",
		"path,url\n/ignored,https://ex.com/a\n",
	} {
		got, err := ReadCSV(strings.NewReader(in), "")
		if err != nil || !reflect.DeepEqual(got, []string{"https://ex.com/a"}) {
			t.Errorf("%q: got %v, %v", in, got, err)
		}
	}
	// Two path columns: the first one is used.
	got, err := ReadCSV(strings.NewReader("path,path\n/a,/b\n"), "https://ex.com")
	if err != nil || !reflect.DeepEqual(got, []string{"https://ex.com/a"}) {
		t.Errorf("two path columns: got %v, %v", got, err)
	}
}

// A record shorter than the header has no value in the URL column: it is
// skipped, not an error and not a panic.
func TestReadCSVShortRecordIsSkipped(t *testing.T) {
	got, err := ReadCSV(strings.NewReader("name,url\nonly-a-name\nb,https://ex.com/b\n"), "")
	if err != nil || !reflect.DeepEqual(got, []string{"https://ex.com/b"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	got, err = ReadCSV(strings.NewReader("name,url\nx,\n y , https://ex.com/c \n"), "")
	if err != nil || !reflect.DeepEqual(got, []string{"https://ex.com/c"}) {
		t.Fatalf("blank value: got %v, %v", got, err)
	}
}

// A sitemap answer is accepted for 2xx only: 300 is an error, 200 and 299 are
// read. (A 199 cannot be tested: net/http treats 1xx as interim.)
func TestReadSitemapAcceptsOnly2xx(t *testing.T) {
	const body = `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://example.com/a</loc></url></urlset>`
	for _, status := range []int{200, 201, 299, 300, 301, 400, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(body))
		}))
		urls, err := Fetcher{Client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, UserAgent: "test"}.
			ReadSitemap(context.Background(), srv.URL, 0, nil)
		srv.Close()
		if status >= 200 && status <= 299 {
			if err != nil || len(urls) != 1 {
				t.Errorf("status %d: urls %v, err %v; want the sitemap read", status, urls, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
			t.Errorf("status %d: urls %v, err %v; want HTTP %d", status, urls, err, status)
		}
	}
}

// Octets from 0x7F up are escaped, 0x80 and 0x7F included; 0x7E ("~") is not.
func TestNormalizeEscapesRawOctets(t *testing.T) {
	for in, want := range map[string]string{
		"/\x80": "/%80",
		"/\xff": "/%FF",
		"/\x7f": "/%7F",
		"/\x7e": "/~",
		"/\x20": "/%20",
		"/\x21": "/!",
	} {
		if got := normalizeEscapes(in); got != want {
			t.Errorf("normalizeEscapes(%q) = %q, want %q", in, got, want)
		}
	}
}

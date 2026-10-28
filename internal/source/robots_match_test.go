package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Patterns and paths from RFC 9309 §2.2.3 and §5.2, plus the edges of the
// matcher: where "*" matches nothing, several "*", "$" on the last part, and
// patterns that must not match.
func TestMatchRobotsPatternTable(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		// Plain prefixes; matching is case-sensitive.
		{"/", "/", true},
		{"/", "/anything", true},
		{"/fish", "/fish", true},
		{"/fish", "/fish.html", true},
		{"/fish", "/fish/salmon.html", true},
		{"/fish", "/fish.php?id=anything", true},
		{"/fish", "/Fish.asp", false},
		{"/fish", "/catfish", false},
		{"/fish", "/?id=fish", false},
		{"/fish/", "/fish", false},
		{"/fish/", "/fish/?id=anything", true},

		// An empty pattern matches every path.
		{"", "/", true},
		{"", "/x?y=1", true},

		// "*" matches any run of characters, including none.
		{"/fish*", "/fish", true},
		{"/fish*", "/fishheads", true},
		{"/fish*", "/fishheads/yummy.html", true},
		{"/fish*", "/fi", false},
		{"*", "/x", true},
		{"/*", "/", true},
		{"**", "/x", true},
		{"/a**b", "/ab", true},
		{"/a**b", "/axb", true},
		{"/*.php", "/index.php", true},
		{"/*.php", "/folder/filename.php", true},
		{"/*.php", "/folder/filename.php?parameters", true},
		{"/*.php", "/", false},
		{"/*.php", "/windows.PHP", false},
		{"*.php", "/index.php", true},
		{"/fish*.php", "/fish.php", true},
		{"/fish*.php", "/fishheads/catfish.php?parameters", true},
		{"/fish*.php", "/Fish.PHP", false},
		{"/a*b", "/ab", true}, // the part follows immediately, at offset 0
		{"/a*b", "/axb", true},
		{"/a*b", "/a", false},
		{"/a*b", "/ba", false},

		// Several "*": each part follows the previous one, in order.
		{"/a*b*c", "/abc", true},
		{"/a*b*c", "/axbxc", true},
		{"/a*b*c", "/axbxcx", true},
		{"/a*b*c", "/acb", false},
		{"/a*b*c", "/ab", false},
		{"/a*b*c", "/abbc", true},
		{"/a*bb*c", "/abxbc", false},
		{"/a*bb*c", "/abbxc", true},

		// "$" anchors the end of the path, query string included.
		{"/$", "/", true},
		{"/$", "/x", false},
		{"/a$", "/a", true},
		{"/a$", "/ab", false},
		{"/a$", "/a?x=1", false},
		{"/*.php$", "/filename.php", true},
		{"/*.php$", "/folder/filename.php", true},
		{"/*.php$", "/filename.php?parameters", false},
		{"/*.php$", "/filename.php/", false},
		{"/*.php$", "/filename.php5", false},
		{"/*.php$", "/php/", false},
		{"/*.php$", "/windows.PHP", false},
		{"/*$", "/x", true},
		{"/a*b$", "/ab", true},
		{"/a*b$", "/axb", true},
		{"/a*b$", "/axbxb", true}, // the last part is matched at the end, not at its first occurrence
		{"/a*b$", "/axbx", false},
		{"/a*b*c$", "/abc", true},
		{"/a*b*c$", "/axbxc", true},
		{"/a*b*c$", "/axbxcx", false},
		{"/a*b*c$", "/axbxcxc", true},
		{"/a*b*c$", "/acb", false},
		{"/a*b*c$", "/ac", false},
		{"/a$b", "/a$b", true}, // "$" is an anchor only at the end of the pattern
		{"/a$b", "/ab", false},
	} {
		if got := matchRobots(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchRobots(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// Longest matching pattern wins; on a tie Allow wins, whatever the order; no
// matching rule allows.
func TestAllowedLongestMatchWins(t *testing.T) {
	allow := func(p string) robotsRule { return robotsRule{allow: true, pattern: p} }
	deny := func(p string) robotsRule { return robotsRule{pattern: p} }
	for _, tc := range []struct {
		name  string
		rules []robotsRule
		path  string
		want  bool
	}{
		{"no rules", nil, "/x", true},
		{"no rule matches", []robotsRule{deny("/private")}, "/public", true},
		{"disallow everything", []robotsRule{deny("/")}, "/x", false},
		{"disallow everything, root", []robotsRule{deny("/")}, "/", false},
		{"empty pattern disallows", []robotsRule{deny("")}, "/x", false},
		{"empty pattern allows", []robotsRule{allow("")}, "/x", true},
		{"longer allow beats disallow", []robotsRule{deny("/folder/"), allow("/folder/page")}, "/folder/page", true},
		{"longer allow, other path", []robotsRule{deny("/folder/"), allow("/folder/page")}, "/folder/other", false},
		{"longer disallow beats allow", []robotsRule{allow("/p"), deny("/private")}, "/private/x", false},
		{"longer disallow, other path", []robotsRule{allow("/p"), deny("/private")}, "/pub", true},
		{"tie goes to allow", []robotsRule{deny("/a"), allow("/a")}, "/a", true},
		{"tie goes to allow, other order", []robotsRule{allow("/a"), deny("/a")}, "/a", true},
		{"tie goes to allow among others", []robotsRule{deny("/a"), deny("/b"), allow("/a"), deny("/c")}, "/a", true},
		{"length counts pattern characters", []robotsRule{allow("/*.html"), deny("/p*")}, "/pg.html", true},
		{"length counts pattern characters, deny", []robotsRule{allow("/*.h"), deny("/page*")}, "/page.h", false},
		{"order does not matter", []robotsRule{allow("/folder/page"), deny("/folder/")}, "/folder/page", true},
		{"unmatched longer rule is ignored", []robotsRule{deny("/folder/page.html$"), allow("/folder")}, "/folder/page.htmlx", true},
	} {
		if got := allowed(tc.rules, tc.path); got != tc.want {
			t.Errorf("%s: allowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A request path is its escaped path plus the query string, compared to the
// patterns as written in robots.txt.
func TestRobotsAllowedUsesEscapedPathAndQuery(t *testing.T) {
	const body = `User-agent: *
Disallow: /private$
Disallow: /*?sid=
Disallow: /a%2Fb
Disallow: /caf%C3%A9
Disallow: /empty
Allow: /empty/open
Disallow:
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	robots := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots()
	for path, want := range map[string]bool{
		"/":                    true,
		"/private":             false,
		"/private/":            true,
		"/private?x=1":         true, // "$" counts the query string
		"/page":                true,
		"/page?sid=1":          false,
		"/page?x=1&sid=1":      true, // "?sid=" is literal
		"/page?x=1?sid=1":      false,
		"/a%2Fb":               false, // an encoded slash is not a slash
		"/a/b":                 true,
		"/caf%C3%A9":           false,
		"/café":                false, // matched in its escaped form
		"/cafe":                true,
		"/empty":               false,
		"/empty/open":          true,
		"/empty/closed":        false,
		"/emptyish":            false,
		"/other/empty":         true,
		"/page?x=%2F":          true,
		"/search/a%20b":        true,
		"/private%2Fsub":       true, // not the same as /private$
		"/private/../private?": true,
	} {
		ok, err := robots.Allowed(context.Background(), srv.URL+path)
		if err != nil || ok != want {
			t.Errorf("Allowed(%q) = %v, %v; want %v", path, ok, err, want)
		}
	}
	if w := robots.Warnings(); len(w) != 0 {
		t.Errorf("warnings %v", w)
	}
}

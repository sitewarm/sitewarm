package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// RFC 9309 §2.2.2: octets are compared percent-encoded, so spellings of one
// path are one path. Unreserved octets (ALPHA DIGIT - . _ ~) are the same
// whether encoded or not, and the hex digits of an escape are case-insensitive.
// Reserved octets are never decoded: %2F is not "/".
func TestRobotsPercentEncodingEquivalence(t *testing.T) {
	for _, tc := range []struct {
		rule, path string
		blocked    bool
	}{
		// "~" and %7E, both directions, both hex cases.
		{"/~user", "/%7Euser", true},
		{"/~user", "/%7euser", true},
		{"/%7Euser", "/~user", true},
		{"/%7euser", "/~user", true},
		{"/%7Euser", "/%7euser", true},
		{"/~user", "/~user", true},
		// Letters and digits written as escapes.
		{"/%61bc", "/abc", true},
		{"/abc", "/%61bc", true},
		{"/%41bc", "/Abc", true},
		{"/%41bc", "/abc", false}, // still case-sensitive
		{"/item%31", "/item1", true},
		{"/a%2Dz", "/a-z", true},
		{"/a-z", "/a%2dz", true},
		{"/a%2Ez", "/a.z", true},
		{"/a%5Fz", "/a_z", true},
		// Hex case of escapes that stay escaped.
		{"/%e2%82%ac", "/%E2%82%AC", true},
		{"/%E2%82%AC", "/%e2%82%ac", true},
		{"/caf%c3%a9", "/café", true},
		{"/café", "/caf%C3%A9", true},
		{"/a%2fb", "/a%2Fb", true},
		{"/a%2Fb", "/a%2fb", true},
		// Reserved octets are never decoded.
		{"/a%2Fb", "/a/b", false},
		{"/a/b", "/a%2Fb", false},
		{"/a%2fb", "/a/b", false},
		{"/p%3Fx", "/p?x", false},
		{"/p?x", "/p%3Fx", false},
		{"/a%23b", "/a%23b", true},
		{"/a%23b", "/a", false},
		{"/a%26b", "/a&b", false},
		{"/a&b", "/a%26b", false},
		// "*" and "$" keep their meaning after normalisation.
		{"/*~$", "/x%7E", true},
		{"/*%7E$", "/x~", true},
		{"/*%7E$", "/x~y", false},
		{"/~$", "/%7E", true},
		{"/~$", "/%7Ey", false},
		{"/%7E*", "/~anything", true},
		{"/*%2F*.php", "/a%2Fb.php", true},
		{"/*%2F*.php", "/a/b.php", false},
		// What matched before still matches: a pattern that ends inside an
		// escape is compared as written.
		{"/a%", "/a%41", true},
		{"/a%4", "/a%41", true},
		{"/a%41", "/a%41", true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("User-agent: *\nDisallow: " + tc.rule + "\n"))
		}))
		ok, err := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots().Allowed(context.Background(), srv.URL+tc.path)
		srv.Close()
		if err != nil || ok == tc.blocked {
			t.Errorf("Disallow %q, URL path %q: allowed = %v, %v; want blocked = %v", tc.rule, tc.path, ok, err, tc.blocked)
		}
	}
}

func TestNormalizeEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"/plain/path":       "/plain/path",
		"/%7Euser":          "/~user",
		"/%7euser":          "/~user",
		"/%61%62%63":        "/abc",
		"/%41%5A%7a%30%39":  "/AZz09",
		"/%2D%2E%5F%7E":     "/-._~",
		"/%2d%2e%5f":        "/-._",
		"/a%2fb":            "/a%2Fb",
		"/a%2Fb":            "/a%2Fb",
		"/p%3fx%3Fy":        "/p%3Fx%3Fy",
		"/a%23b%26c%25d":    "/a%23b%26c%25d",
		"/%e2%82%ac":        "/%E2%82%AC",
		"/café":             "/caf%C3%A9",
		"/caf%c3%a9":        "/caf%C3%A9",
		"/a b":              "/a b",
		"/a%20b":            "/a%20b",
		"/%2A%24":           "/%2A%24", // an escaped * or $ stays an escape
		"/*.php$":           "/*.php$",
		"/a%":               "/a%",
		"/a%4":              "/a%4",
		"/a%4g":             "/a%4g",
		"/a%zz":             "/a%zz",
		"%":                 "%",
		"%7":                "%7",
		"%7E":               "~",
		"/%7E/%7e?q=%7E&r=": "/~/~?q=~&r=",
	} {
		if got := normalizeEscapes(in); got != want {
			t.Errorf("normalizeEscapes(%q) = %q, want %q", in, got, want)
		}
		if again := normalizeEscapes(want); again != want {
			t.Errorf("normalizeEscapes is not idempotent on %q: %q", want, again)
		}
	}
}

// The longest match is measured on the normalised pattern, so two spellings
// of one pattern compete as equals.
func TestRobotsPercentEncodingLongestMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /~u\nAllow: /%7Eu\n"))
	}))
	defer srv.Close()
	// Same pattern, spelled two ways: a tie, and Allow wins a tie.
	for _, path := range []string{"/~u", "/%7Eu", "/%7eu/x"} {
		ok, err := Fetcher{Client: srv.Client(), UserAgent: "test"}.NewRobots().Allowed(context.Background(), srv.URL+path)
		if err != nil || !ok {
			t.Errorf("Allowed(%q) = %v, %v; want allowed (tie goes to Allow)", path, ok, err)
		}
	}
}

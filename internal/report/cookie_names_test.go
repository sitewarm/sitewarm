package report

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sitewarm/sitewarm/internal/probe"
	"github.com/sitewarm/sitewarm/internal/verify"
)

func cookieEntry(setCookie ...string) Entry {
	h := map[string][]string{}
	if setCookie != nil {
		h["Set-Cookie"] = setCookie
	}
	return entry(verify.Outcome{Last: &probe.Observation{Header: h, Status: 200}})
}

func TestSetCookieNames(t *testing.T) {
	n64 := strings.Repeat("n", 64)
	var twelve, first10 []string
	for i := 1; i <= 12; i++ {
		twelve = append(twelve, fmt.Sprintf("c%02d=x", i))
		if i <= 10 {
			first10 = append(first10, fmt.Sprintf("c%02d", i))
		}
	}
	wp := "wordpress_logged_in_0123456789abcdef0123456789abcdef"
	for name, tc := range map[string]struct {
		headers []string
		cookie  bool
		want    []string
	}{
		"C1 none":                     {nil, false, nil},
		"C2 attributes":               {[]string{"a=1; Path=/; HttpOnly"}, true, []string{"a"}},
		"C3 twelve capped at ten":     {twelve, true, first10},
		"C4 dedupe keeps first":       {[]string{"a=1", "b=2", "a=3"}, true, []string{"a", "b"}},
		"C4 case sensitive":           {[]string{"A=1", "a=2"}, true, []string{"A", "a"}},
		"C5 65 bytes dropped":         {[]string{strings.Repeat("n", 65) + "=v"}, true, nil},
		"C5 64 bytes kept":            {[]string{n64 + "=v"}, true, []string{n64}},
		"C6 not redacted":             {[]string{wp + "=v"}, true, []string{wp}},
		"C7 invalid names skipped":    {[]string{"=novalue", "  =x", "flagonly", "bad name=1", "a;b=1", "ok=1"}, true, []string{"ok"}},
		"C7 spaces trimmed":           {[]string{"  sid  =abc"}, true, []string{"sid"}},
		"C7 tabs trimmed":             {[]string{"\tsid\t=abc"}, true, []string{"sid"}},
		"C10 second equals":           {[]string{"t=a=b=c"}, true, []string{"t"}},
		"separators in name":          {[]string{`a"b=1`, "a/b=1", "a[b]=1", "a{b}=1", "a(b)=1", "a<b>=1", "a@b=1", "a,b=1", "a:b=1", `a\b=1`, "a?b=1", "ok=1"}, true, []string{"ok"}},
		"control and non-ASCII bytes": {[]string{"a\x01b=1", "a\x7fb=1", "café=1", "ok=1"}, true, []string{"ok"}},
		"only invalid keeps bool":     {[]string{"flagonly", "=x"}, true, nil},
		"cap counts kept names only":  {append([]string{"=x", "bad name=1", strings.Repeat("n", 65) + "=v"}, twelve...), true, first10},
		"token punctuation allowed":   {[]string{"a-b_c.d!#$%&'*+^`|~=1"}, true, []string{"a-b_c.d!#$%&'*+^`|~"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := cookieEntry(tc.headers...)
			if e.SetCookie != tc.cookie {
				t.Errorf("SetCookie = %v, want %v", e.SetCookie, tc.cookie)
			}
			if len(e.SetCookieNames) != len(tc.want) || (len(tc.want) > 0 && !reflect.DeepEqual(e.SetCookieNames, tc.want)) {
				t.Errorf("SetCookieNames = %q, want %q", e.SetCookieNames, tc.want)
			}
		})
	}
}

func TestSetCookieNamesJSON(t *testing.T) {
	b, err := json.Marshal(cookieEntry())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "set_cookie_names") || !strings.Contains(string(b), `"set_cookie":false`) {
		t.Errorf("entry without cookies: %s", b)
	}
	b, _ = json.Marshal(cookieEntry("a=1", "b=2"))
	if !strings.Contains(string(b), `"set_cookie":true`) || !strings.Contains(string(b), `"set_cookie_names":["a","b"]`) {
		t.Errorf("entry with names: %s", b)
	}
	// A cookie header whose name is not usable keeps the bool and omits the list.
	b, _ = json.Marshal(cookieEntry("flagonly"))
	if !strings.Contains(string(b), `"set_cookie":true`) || strings.Contains(string(b), "set_cookie_names") {
		t.Errorf("entry with an unusable name: %s", b)
	}
}

// Only names leave the parse: not the value, not the attributes.
func TestSetCookieValuesAndAttributesNeverReported(t *testing.T) {
	e := cookieEntry("s=SECRETVALUE123; Domain=example.com; Path=/p; Expires=Wed, 21 Oct 2026 07:28:00 GMT")
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SECRETVALUE123", "example.com", "/p", "Expires", "21 Oct", "Domain"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("%q leaked into %s", leak, b)
		}
	}
	if !strings.Contains(string(b), `"set_cookie_names":["s"]`) {
		t.Errorf("name missing: %s", b)
	}
	// The report entry holds the names only, whatever the reporters print.
	if got := fmt.Sprintf("%+v", e); strings.Contains(got, "SECRETVALUE123") {
		t.Errorf("value in the entry: %s", got)
	}
}

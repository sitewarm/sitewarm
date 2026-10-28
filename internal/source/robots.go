package source

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
)

// RobotsToken is the product token matched against robots.txt groups. The
// "sitewarm" group applies; without one, the "*" group does.
const RobotsToken = "sitewarm"

// maxRobotsBytes is how much of a robots.txt is parsed, the minimum that
// RFC 9309 requires; the rest is ignored.
const maxRobotsBytes = 500 << 10

// Robots checks URLs against the robots.txt of their host, fetched once per
// scheme and host. It is safe for concurrent use.
//
//   - 2xx: the rules apply.
//   - 4xx (401, 403, 404, 410...): everything is allowed, as RFC 9309 says.
//   - 5xx, any other status, a timeout or a network error: robots.txt is
//     unreachable and every URL of that host is skipped, with a warning.
type Robots struct {
	f        Fetcher
	mu       sync.Mutex
	hosts    map[string]*robotsHost
	loading  map[string]*robotsLoad // first loads in flight, one per host
	warnings []string
}

// robotsLoad is closed when the first load of a host ends, stored or not.
type robotsLoad struct{ done chan struct{} }

type robotsHost struct {
	rules       []robotsRule // nil allows everything
	unavailable bool
}

type robotsRule struct {
	allow   bool
	pattern string
	norm    string // pattern after normalizeEscapes; empty means not computed
}

// NewRobots returns a checker that fetches with f.
func (f Fetcher) NewRobots() *Robots {
	return &Robots{f: f, hosts: map[string]*robotsHost{}, loading: map[string]*robotsLoad{}}
}

// Allowed reports whether rawURL may be requested.
func (r *Robots) Allowed(ctx context.Context, rawURL string) (bool, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, err
	}
	key := u.Scheme + "://" + u.Host
	for {
		r.mu.Lock()
		if h, ok := r.hosts[key]; ok {
			r.mu.Unlock()
			if h.unavailable {
				return false, nil
			}
			return allowed(h.rules, robotsPath(u)), nil
		}
		if l, busy := r.loading[key]; busy {
			// Another lookup is fetching this host's robots.txt: wait for it
			// instead of sending a second request.
			r.mu.Unlock()
			select {
			case <-l.done:
			case <-ctx.Done():
				return false, ctx.Err()
			}
			continue // stored, or the loader was canceled and this one loads
		}
		l := &robotsLoad{done: make(chan struct{})}
		r.loading[key] = l
		r.mu.Unlock()

		var h *robotsHost
		func() {
			// Deferred so waiters are released even if the load panics.
			defer func() {
				r.mu.Lock()
				if h != nil {
					r.hosts[key] = h
				}
				delete(r.loading, key)
				r.mu.Unlock()
				close(l.done)
			}()
			h = r.load(ctx, u)
		}()

		if h == nil {
			// Canceled or past the run deadline: says nothing about the host.
			return false, ctx.Err()
		}
	}
}

// Warnings lists one message per host whose robots.txt was unavailable.
func (r *Robots) Warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.warnings...)
}

func (r *Robots) warn(format string, args ...any) {
	r.mu.Lock()
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

// load returns nil when ctx ended before robots.txt was read.
func (r *Robots) load(ctx context.Context, u *url.URL) *robotsHost {
	robotsURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}).String()
	resp, err := r.f.get(ctx, robotsURL)
	if err != nil && ctx.Err() != nil {
		return nil
	}
	if err != nil {
		r.warn("%s could not be fetched (%v); its sitemap URLs are skipped-robots", robotsURL, err)
		return &robotsHost{unavailable: true}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 400 && resp.StatusCode <= 499:
		// RFC 9309 §2.3.1.3: robots.txt "unavailable" allows everything.
		return &robotsHost{}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		r.warn("%s returned HTTP %d; its sitemap URLs are skipped-robots", robotsURL, resp.StatusCode)
		return &robotsHost{unavailable: true}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRobotsBytes))
	if err != nil && ctx.Err() != nil {
		return nil
	}
	if err != nil {
		r.warn("%s could not be read (%v); its sitemap URLs are skipped-robots", robotsURL, err)
		return &robotsHost{unavailable: true}
	}
	return &robotsHost{rules: parseRobots(data, RobotsToken)}
}

// parseRobots returns the rules of the groups for token, or of the "*"
// groups when none names token. Groups for the same agent are merged.
func parseRobots(data []byte, token string) []robotsRule {
	type group struct {
		agents []string
		rules  []robotsRule
	}
	var groups []*group
	lastWasAgent := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), maxRobotsBytes)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if !lastWasAgent || len(groups) == 0 {
				groups = append(groups, &group{})
			}
			g := groups[len(groups)-1]
			g.agents = append(g.agents, strings.ToLower(value))
			lastWasAgent = true
		case "allow", "disallow":
			lastWasAgent = false
			if len(groups) == 0 || value == "" {
				continue
			}
			g := groups[len(groups)-1]
			g.rules = append(g.rules, robotsRule{allow: key == "allow", pattern: value, norm: normalizeEscapes(value)})
		}
	}
	collect := func(agent string) (rules []robotsRule, found bool) {
		for _, g := range groups {
			for _, a := range g.agents {
				if a == agent {
					rules = append(rules, g.rules...)
					found = true
					break
				}
			}
		}
		return rules, found
	}
	if rules, found := collect(strings.ToLower(token)); found {
		return rules
	}
	rules, _ := collect("*")
	return rules
}

// allowed applies the longest matching rule; on a tie, allow wins. No
// matching rule allows.
//
// A rule matches when its pattern as written matches the path as written (as
// it always did), or when the two match after normalizeEscapes, which adds the
// other spellings of the same path. Its length is that of the longer form that
// matched, so a rule never ranks lower than it did before.
func allowed(rules []robotsRule, path string) bool {
	best, ok := -1, true
	norm := normalizeEscapes(path)
	for _, r := range rules {
		n := -1
		if matchRobots(r.pattern, path) {
			n = len(r.pattern)
		}
		if pattern := r.normalized(); n < 0 || pattern != r.pattern {
			if matchRobots(pattern, norm) {
				n = max(n, len(pattern))
			}
		}
		if n < 0 {
			continue
		}
		if n > best || (n == best && r.allow) {
			best, ok = n, r.allow
		}
	}
	return ok
}

// normalized is the pattern in the spelling of normalizeEscapes.
func (r robotsRule) normalized() string {
	if r.norm != "" || r.pattern == "" {
		return r.norm
	}
	return normalizeEscapes(r.pattern)
}

// normalizeEscapes gives paths and patterns one spelling (RFC 9309 §2.2.2):
//   - an escaped unreserved octet (ALPHA DIGIT - . _ ~) is decoded;
//   - the hex digits of every other escape are uppercased;
//   - an octet that net/url writes escaped in a path (controls, space,
//     non-ASCII and the characters in escapedInPath) is escaped;
//   - a % that does not start an escape becomes %25, so it can never join the
//     characters after it into one.
//
// Reserved octets such as %2F, %3F and %23 stay escaped, so /a%2Fb is not
// /a/b, and the operators * and $ are not touched.
func normalizeEscapes(s string) string {
	const hex = "0123456789ABCDEF"
	plain := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '%' || mustEscape(c) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			v := unhex(s[i+1])<<4 | unhex(s[i+2])
			if isUnreserved(v) {
				b.WriteByte(v)
			} else {
				b.WriteByte('%')
				b.WriteByte(hex[v>>4])
				b.WriteByte(hex[v&15])
			}
			i += 2
		case c == '%':
			b.WriteString("%25")
		case mustEscape(c):
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// escapedInPath are the printable ASCII octets that net/url escapes in a path
// and that are not reserved.
const escapedInPath = "\"<>\\^`{|}"

func mustEscape(c byte) bool {
	return c <= 0x20 || c >= 0x7F || strings.IndexByte(escapedInPath, c) >= 0
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// matchRobots matches a path against a pattern with the * and $ operators
// of RFC 9309.
func matchRobots(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	for i := 1; i < len(parts); i++ {
		if anchored && i == len(parts)-1 {
			return strings.HasSuffix(path[pos:], parts[i])
		}
		j := strings.Index(path[pos:], parts[i])
		if j < 0 {
			return false
		}
		pos += j + len(parts[i])
	}
	return !anchored || pos == len(path)
}

func robotsPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

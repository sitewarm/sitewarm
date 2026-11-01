// Package report builds the scan report and decides which results fail.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sitewarm/sitewarm/internal/cdn"
	"github.com/sitewarm/sitewarm/internal/probe"
	"github.com/sitewarm/sitewarm/internal/verify"
)

// Tokens accepted by --fail-on besides the cache states.
const (
	NoIndex      = "noindex"
	Inconclusive = string(probe.IndexabilityInconclusive)
)

// DefaultFailOn is the documented --fail-on default.
const DefaultFailOn = "miss,client-error,error,incomplete,noindex,redirect-error"

// FailOn is the set of states (plus noindex tokens) that fail a run.
type FailOn map[string]bool

// ParseFailOn parses a comma-separated --fail-on value. An empty value
// fails on nothing.
func ParseFailOn(s string) (FailOn, error) {
	valid := map[string]bool{NoIndex: true, Inconclusive: true}
	for _, st := range cdn.States {
		if st != cdn.Hit {
			valid[string(st)] = true
		}
	}
	f := FailOn{}
	for _, tok := range strings.Split(s, ",") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if tok == "" {
			continue
		}
		if !valid[tok] {
			return nil, fmt.Errorf("--fail-on: unknown value %q (valid: %s)", tok, strings.Join(validList(), ", "))
		}
		f[tok] = true
	}
	return f, nil
}

func validList() []string {
	var out []string
	for _, st := range cdn.States {
		if st != cdn.Hit {
			out = append(out, string(st))
		}
	}
	return append(out, NoIndex, Inconclusive)
}

// List returns the tokens in a stable order.
func (f FailOn) List() []string {
	var out []string
	for _, tok := range validList() {
		if f[tok] {
			out = append(out, tok)
		}
	}
	return out
}

// Fails reports whether a result fails the run. indexability-error fails
// with noindex: an unreadable body cannot prove the page is indexable.
func (f FailOn) Fails(state cdn.State, idx probe.Indexability) bool {
	switch {
	case f[string(state)]:
		return true
	case f[NoIndex] && (idx == probe.NoIndex || idx == probe.IndexabilityError):
		return true
	case f[Inconclusive] && idx == probe.IndexabilityInconclusive:
		return true
	}
	return false
}

// Entry is the report line for one URL and profile.
type Entry struct {
	URL     string `json:"url"`
	Profile string `json:"profile"`
	// LastMod is the <lastmod> of the URL in the sitemap, as written. It is
	// informational: it never affects a state or the verdict.
	LastMod      string          `json:"lastmod,omitempty"`
	FinalURL     string          `json:"final_url,omitempty"`
	Redirects    []probe.Hop     `json:"redirects,omitempty"`
	InitialState cdn.State       `json:"initial_state,omitempty"`
	State        cdn.State       `json:"state"`
	Failed       bool            `json:"failed"`
	Attempts     int             `json:"attempts"`
	HTTPStatus   int             `json:"http_status,omitempty"`
	CDN          string          `json:"cdn,omitempty"`
	CacheStatus  string          `json:"cache_status_raw,omitempty"`
	POP          string          `json:"pop,omitempty"`
	OtherCDNs    []cdn.Detection `json:"other_cdns,omitempty"`
	TTFBFirstMS  *int64          `json:"ttfb_first_ms"`
	TTFBFinalMS  *int64          `json:"ttfb_final_ms"`
	TimeToHitMS  *int64          `json:"time_to_hit_ms"`
	CacheControl string          `json:"cache_control,omitempty"`
	Age          string          `json:"age,omitempty"`
	Vary         string          `json:"vary,omitempty"`
	// SetCookie records only whether the header was present; its value is
	// never reported.
	SetCookie bool `json:"set_cookie"`
	// SetCookieNames lists the names of the cookies set, never their values or
	// attributes: see cookieNames for the limits.
	SetCookieNames     []string           `json:"set_cookie_names,omitempty"`
	Indexability       probe.Indexability `json:"indexability,omitempty"`
	IndexabilityDetail string             `json:"indexability_detail,omitempty"`
	// Canonical is the absolute canonical URL of the final page, from
	// <link rel="canonical"> in <head>. It is left out when the page has none.
	Canonical string `json:"canonical,omitempty"`
	// Warmed is true when the reported measurement came after the URL had
	// already been requested once in this run, or the first response was
	// already a HIT. It is false for a single request that was not a HIT.
	Warmed   bool     `json:"warmed"`
	Detail   string   `json:"detail,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Summary aggregates the entries.
type Summary struct {
	Total    int               `json:"total"`
	ByState  map[cdn.State]int `json:"by_state"`
	HitRatio float64           `json:"hit_ratio"`
	Failed   int               `json:"failed"`
	Passed   bool              `json:"passed"`
	Verdict  string            `json:"verdict"`
}

// Settings echoes the options the run used.
type Settings struct {
	Source        string   `json:"source"`
	Top           int      `json:"top"`
	Profiles      []string `json:"profiles"`
	Concurrency   int      `json:"concurrency"`
	RPS           float64  `json:"rps"`
	RetryInterval string   `json:"retry_interval"`
	MaxWait       string   `json:"max_wait"`
	RunTimeout    string   `json:"run_timeout"`
	FailOn        []string `json:"fail_on"`
	// IgnoreRobots is set when --from-sitemap ran without robots.txt.
	IgnoreRobots bool `json:"ignore_robots,omitempty"`
}

// Report is the scan result.
type Report struct {
	Tool       string    `json:"tool"`
	Version    string    `json:"version"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMS int64     `json:"duration_ms"`
	// Stopped is set when the run ended early: run-timeout, SIGINT, SIGTERM
	// or canceled. Unfinished results are incomplete.
	Stopped  string   `json:"stopped,omitempty"`
	Settings Settings `json:"settings"`
	Summary  Summary  `json:"summary"`
	Warnings []string `json:"warnings"`
	Results  []Entry  `json:"results"`
}

// Standing limitations, printed on every report.
const (
	WarnSinglePoP = "verified only at the CDN PoP serving this machine; other PoPs were not checked"
	WarnVariants  = "a cache key that depends on device or User-Agent without saying so in Vary can make a hit unrepresentative of real users"
	verdictPass   = "cache verified; deploy freshness not verified"
)

// Build assembles the report from the scheduler outcomes. Warnings already
// in r (from the URL source) are kept after the standing ones.
func Build(r *Report, outcomes []verify.Outcome, failOn FailOn) {
	r.Tool = "sitewarm"
	r.Settings.FailOn = failOn.List()
	r.DurationMS = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
	r.Warnings = append([]string{WarnSinglePoP, WarnVariants}, r.Warnings...)
	s := Summary{ByState: map[cdn.State]int{}}
	r.Results = make([]Entry, 0, len(outcomes))
	for _, o := range outcomes {
		e := entry(o)
		e.Failed = failOn.Fails(e.State, e.Indexability)
		s.Total++
		s.ByState[e.State]++
		if e.Failed {
			s.Failed++
		}
		r.Results = append(r.Results, e)
	}
	if s.Total > 0 {
		s.HitRatio = float64(s.ByState[cdn.Hit]) / float64(s.Total)
	}
	// A run where nothing was verified never passes, even when
	// skipped-robots is not in --fail-on. That includes an empty run, such
	// as one stopped while the sitemap was being read.
	noneVerified := s.ByState[cdn.SkippedRobots] == s.Total
	s.Passed = s.Failed == 0 && !noneVerified
	s.Verdict = verdict(s, noneVerified)
	r.Summary = s
}

// verdict is the one-line summary of a run.
func verdict(s Summary, noneVerified bool) string {
	switch {
	case s.Failed > 0:
		return fmt.Sprintf("failed: %d of %d results are in --fail-on", s.Failed, s.Total)
	case s.Total == 0:
		return "failed: no URL was verified"
	case noneVerified:
		return fmt.Sprintf("failed: no URL was verified; all %d results are skipped-robots", s.Total)
	case s.ByState[cdn.Unknown] == s.Total-s.ByState[cdn.SkippedRobots]:
		// Every requested result was unrecognized or absent (for example a
		// site with no CDN): unknown stays unknown, and no cache was
		// verified. Skipped-robots results were never requested, so they
		// do not count. The run still passes: unknown is not in the
		// default --fail-on.
		return fmt.Sprintf("no cache state observed: all %d requested results are unknown; deploy freshness not verified", s.ByState[cdn.Unknown])
	default:
		return verdictPass
	}
}

// SkippedOutcome is the outcome of a pair that robots.txt kept from being
// requested.
func SkippedOutcome(p verify.Pair) verify.Outcome {
	return verify.Outcome{
		Pair: p, InitialState: cdn.SkippedRobots, State: cdn.SkippedRobots,
		Verdict: verify.Verdict{Detail: "not requested: robots.txt disallows it or could not be read"},
	}
}

func ms(d time.Duration) *int64 {
	v := d.Milliseconds()
	return &v
}

func entry(o verify.Outcome) Entry {
	e := Entry{
		URL:          o.URL,
		Profile:      o.Profile.Name,
		InitialState: o.InitialState,
		State:        o.State,
		Attempts:     o.Attempts,
		Detail:       o.Verdict.Detail,
	}
	if o.First != nil && o.First.HasTTFB {
		e.TTFBFirstMS = ms(o.First.TTFB)
	}
	if o.GotHit {
		e.TimeToHitMS = ms(o.TimeToHit)
	}
	e.Warmed = o.Attempts > 1 || o.InitialState == cdn.Hit
	last := o.Last
	if last == nil {
		return e
	}
	if last.HasTTFB {
		e.TTFBFinalMS = ms(last.TTFB)
	}
	e.FinalURL = last.FinalURL
	e.Redirects = last.Redirects
	e.HTTPStatus = last.Status
	e.CDN = o.Verdict.CDN.CDN
	e.CacheStatus = o.Verdict.CDN.Raw
	e.POP = o.Verdict.CDN.POP
	e.OtherCDNs = o.Verdict.CDN.Others
	if h := last.Header; h != nil {
		e.CacheControl = strings.Join(h.Values("Cache-Control"), ", ")
		e.Age = h.Get("Age")
		e.Vary = strings.Join(h.Values("Vary"), ", ")
		e.SetCookie = len(h.Values("Set-Cookie")) > 0
		e.SetCookieNames = cookieNames(h.Values("Set-Cookie"))
	}
	e.Indexability = last.Indexability
	e.IndexabilityDetail = last.IndexabilityDetail
	e.Canonical = last.Canonical
	if len(last.Redirects) > 0 && last.RedirectError == "" {
		e.Warnings = append(e.Warnings, fmt.Sprintf("redirected: %s → %s", last.URL, last.FinalURL))
	}
	if e.Indexability == probe.IndexabilityInconclusive {
		e.Warnings = append(e.Warnings, "indexability inconclusive: "+e.IndexabilityDetail)
	}
	return e
}

const (
	maxCookieNames   = 10
	maxCookieNameLen = 64
)

// cookieNames returns the names of the cookies in Set-Cookie header values, in
// header order: the text before the first "=", trimmed of spaces and tabs.
// Only the name is read; the value and the attributes are never kept. A header
// without "=", or whose name is empty or not an RFC 6265 token, adds no name.
// A name over maxCookieNameLen bytes is dropped, not truncated. Names are
// de-duplicated (case-sensitive, first kept) and capped at maxCookieNames.
func cookieNames(values []string) []string {
	var names []string
	for _, v := range values {
		name, _, ok := strings.Cut(v, "=")
		if !ok {
			continue
		}
		name = strings.Trim(name, " \t")
		if name == "" || len(name) > maxCookieNameLen || !isToken(name) || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
		if len(names) == maxCookieNames {
			break
		}
	}
	return names
}

// isToken reports whether s is an RFC 6265 token: visible ASCII without the
// separators ()<>@,;:\"/[]?={}.
func isToken(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`()<>@,;:\"/[]?={}`, c) >= 0 {
			return false
		}
	}
	return true
}

// WriteJSON writes the report as indented JSON.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// WriteCSV writes one row per result with the columns url,status,state.
// status is the HTTP status code, empty when there was no response.
func WriteCSV(w io.Writer, r *Report) error {
	cw := csv.NewWriter(w)
	cw.Write([]string{"url", "status", "state"})
	for _, e := range r.Results {
		status := ""
		if e.HTTPStatus != 0 {
			status = strconv.Itoa(e.HTTPStatus)
		}
		cw.Write([]string{e.URL, status, string(e.State)})
	}
	cw.Flush()
	return cw.Error()
}

// attention lists the states that do not fail by default but are
// highlighted because they will never become a hit.
var attention = map[cdn.State]bool{
	cdn.Bypass: true, cdn.Unknown: true, cdn.VariantUnverifiable: true,
	cdn.Challenged: true, cdn.Stale: true,
}

// WriteTable writes a human-readable summary. Failed rows are marked ✗ and
// highlighted states !.
func WriteTable(w io.Writer, r *Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tPROFILE\tURL\tCDN\tPOP\tTTFB 1st→last\tTO HIT\tINDEX\t")
	for _, e := range r.Results {
		mark := ""
		switch {
		case e.Failed:
			mark = " ✗"
		case attention[e.State]:
			mark = " !"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\t%s→%s\t%s\t%s\t\n",
			e.State, mark, e.Profile, e.URL, dash(e.CDN), dash(e.POP),
			msText(e.TTFBFirstMS), msText(e.TTFBFinalMS), msText(e.TimeToHitMS), dash(string(e.Indexability)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w)
	var counts []string
	for _, st := range cdn.States {
		if n := r.Summary.ByState[st]; n > 0 {
			counts = append(counts, fmt.Sprintf("%s=%d", st, n))
		}
	}
	fmt.Fprintf(w, "%d results: %s (hit ratio %.0f%%)\n", r.Summary.Total, strings.Join(counts, " "), r.Summary.HitRatio*100)
	var notable []string
	for _, st := range cdn.States {
		if n := r.Summary.ByState[st]; n > 0 && attention[st] {
			notable = append(notable, fmt.Sprintf("%s=%d", st, n))
		}
	}
	if len(notable) > 0 {
		fmt.Fprintf(w, "attention (not failing unless in --fail-on): %s\n", strings.Join(notable, " "))
	}
	var stale []string
	for _, e := range r.Results {
		if e.State == cdn.Stale {
			stale = append(stale, e.URL+" ("+e.Profile+")")
		}
	}
	if len(stale) > 0 {
		fmt.Fprintf(w, "stale after --max-wait: %s\n", strings.Join(stale, ", "))
	}
	if r.Stopped != "" {
		fmt.Fprintf(w, "stopped early: %s\n", r.Stopped)
	}
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "note: %s\n", warn)
	}
	_, err := fmt.Fprintln(w, r.Summary.Verdict)
	return err
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func msText(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%dms", *v)
}

package verify

import (
	"net/http"
	"testing"

	"github.com/sitewarm/sitewarm/internal/cdn"
	"github.com/sitewarm/sitewarm/internal/probe"
)

// Every status class has an edge on each side. Statuses are classified on
// their own: the cache header says HIT, which must not rescue a non-2xx.
func TestClassifyStatusBoundaries(t *testing.T) {
	hit := http.Header{"Cf-Cache-Status": {"HIT"}}
	for _, tc := range []struct {
		status int
		want   cdn.State
	}{
		{0, cdn.Unknown},
		{100, cdn.Unknown},
		{101, cdn.Unknown},
		{199, cdn.Unknown},
		{200, cdn.Hit},
		{204, cdn.Hit},
		{299, cdn.Hit},
		{300, cdn.RedirectError},
		{304, cdn.RedirectError},
		{305, cdn.RedirectError},
		{399, cdn.RedirectError},
		{400, cdn.ClientError},
		{404, cdn.ClientError},
		{429, cdn.ClientError},
		{499, cdn.ClientError},
		{500, cdn.Error},
		{503, cdn.Error},
		{599, cdn.Error},
	} {
		got := Classify(probe.Observation{Status: tc.status, Header: hit})
		if got.State != tc.want {
			t.Errorf("status %d: got %s, want %s", tc.status, got.State, tc.want)
		}
	}
}

// An unfollowed 3xx and an informational status carry the status text and the
// parsed CDN result, so the report can say why.
func TestClassifyNonFinalStatusesKeepDetail(t *testing.T) {
	h := http.Header{"Cf-Cache-Status": {"HIT"}}
	for _, status := range []int{300, 304, 100} {
		v := Classify(probe.Observation{Status: status, Header: h})
		if v.Detail != http.StatusText(status) || v.CDN.State != cdn.Hit {
			t.Errorf("status %d: detail %q, CDN state %s", status, v.Detail, v.CDN.State)
		}
	}
}

// 403 is a challenge whatever the cache header says; 401 is a client error.
func TestClassifyForbiddenIsChallengedButUnauthorizedIsNot(t *testing.T) {
	if got := Classify(probe.Observation{Status: 403, Header: http.Header{}}).State; got != cdn.Challenged {
		t.Errorf("403 = %s", got)
	}
	if got := Classify(probe.Observation{Status: 401, Header: http.Header{}}).State; got != cdn.ClientError {
		t.Errorf("401 = %s", got)
	}
}

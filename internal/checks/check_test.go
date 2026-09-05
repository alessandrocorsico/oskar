package checks

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func names(cs []Check) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name())
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSelect(t *testing.T) {
	all, err := Select(nil, nil)
	if err != nil || len(all) != len(All()) {
		t.Fatalf("Select(nil, nil) = %v, %v", names(all), err)
	}

	only, err := Select([]string{"cert-expiry", "ghost-endpoints", "cert-expiry"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(only); !equalStrings(got, []string{"cert-expiry", "ghost-endpoints"}) {
		t.Fatalf("--checks must keep order and drop duplicates, got %v", got)
	}

	skipped, err := Select(nil, []string{"cert-expiry"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names(skipped) {
		if n == "cert-expiry" {
			t.Fatal("--skip-checks was not honored")
		}
	}
	if len(skipped) != len(All())-1 {
		t.Fatalf("expected %d checks, got %d", len(All())-1, len(skipped))
	}

	if _, err := Select([]string{"nope"}, nil); err == nil {
		t.Fatal("unknown --checks name must be rejected")
	}
	if _, err := Select(nil, []string{"nope"}); err == nil {
		t.Fatal("unknown --skip-checks name must be rejected")
	}
}

func TestEveryCheckDeclaresNeeds(t *testing.T) {
	for _, c := range All() {
		if len(c.Needs()) == 0 {
			t.Errorf("check %s declares no needs", c.Name())
		}
		if c.Description() == "" {
			t.Errorf("check %s has no description", c.Name())
		}
	}
}

func TestNeededMerges(t *testing.T) {
	needs := Needed([]Check{&GhostEndpoints{}, &DeadWebhook{}})
	byRes := map[cluster.Resource]cluster.Need{}
	for _, n := range needs {
		if _, dup := byRes[n.Resource]; dup {
			t.Fatalf("resource %s listed twice", n.Resource)
		}
		byRes[n.Resource] = n
	}
	// Services: optional for ghost-endpoints, required cluster-wide for dead-webhook.
	svc := byRes[cluster.ResourceServices]
	if !svc.ClusterWide || svc.Optional {
		t.Fatalf("services should merge to required+cluster-wide, got %+v", svc)
	}
	// EndpointSlices: required by both, cluster-wide by dead-webhook.
	es := byRes[cluster.ResourceEndpointSlices]
	if !es.ClusterWide || es.Optional {
		t.Fatalf("endpointslices should merge to required+cluster-wide, got %+v", es)
	}
	if pods := byRes[cluster.ResourcePods]; pods.ClusterWide || pods.Optional {
		t.Fatalf("pods should stay required and namespaced, got %+v", pods)
	}
}

func TestFingerprintStableAcrossMessages(t *testing.T) {
	a := Finding{Check: "x", Namespace: "ns", Kind: "Pod", Name: "p", Message: "stuck for 2h"}
	b := a
	b.Message = "stuck for 3h"
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("fingerprint must not depend on the message")
	}
	c := a
	c.Key = "other"
	if Fingerprint(a) == Fingerprint(c) {
		t.Fatal("fingerprint must depend on the key")
	}
	d := a
	d.Related = []Ref{{Kind: "Service", Name: "s"}}
	if Fingerprint(a) == Fingerprint(d) {
		t.Fatal("fingerprint must depend on related objects")
	}
	if len(Fingerprint(a)) != 16 {
		t.Fatalf("unexpected fingerprint length: %q", Fingerprint(a))
	}
}

func TestSortOrdersBySeverityThenObject(t *testing.T) {
	fs := []Finding{
		{Severity: SeverityInfo, Namespace: "a", Kind: "Pod", Name: "z"},
		{Severity: SeverityCritical, Namespace: "b", Kind: "Pod", Name: "y"},
		{Severity: SeverityCritical, Namespace: "a", Kind: "Pod", Name: "x", Key: "2"},
		{Severity: SeverityCritical, Namespace: "a", Kind: "Pod", Name: "x", Key: "1"},
		{Severity: SeverityWarning, Namespace: "a", Kind: "Pod", Name: "w"},
	}
	Sort(fs)
	want := []string{"x/1", "x/2", "y/", "w/", "z/"}
	for i, f := range fs {
		if got := f.Name + "/" + f.Key; got != want[i] {
			t.Fatalf("position %d: got %s, want %s (order: %v)", i, got, want[i], fs)
		}
	}
}

type fakeCheck struct {
	name  string
	needs []cluster.Need
	out   []Finding
	err   error
}

func (f *fakeCheck) Name() string          { return f.name }
func (f *fakeCheck) Description() string   { return "fake" }
func (f *fakeCheck) Needs() []cluster.Need { return f.needs }
func (f *fakeCheck) Run(context.Context, *cluster.Snapshot, Options) ([]Finding, error) {
	return f.out, f.err
}

func TestRunAllStatusesAndFingerprints(t *testing.T) {
	ok := &fakeCheck{name: "ok", out: []Finding{
		{Check: "ok", Severity: SeverityWarning, Kind: "Pod", Name: "b"},
		{Check: "ok", Severity: SeverityCritical, Kind: "Pod", Name: "a"},
	}}
	broken := &fakeCheck{name: "broken", err: errors.New("boom")}
	fs, statuses := RunAll(context.Background(), []Check{ok, broken}, &cluster.Snapshot{}, Options{})

	if len(fs) != 2 || fs[0].Severity != SeverityCritical {
		t.Fatalf("findings should be collected and sorted, got %+v", fs)
	}
	for _, f := range fs {
		if f.Fingerprint == "" {
			t.Fatalf("fingerprint missing on %+v", f)
		}
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %+v", statuses)
	}
	if statuses[0] != (Status{Name: "ok", State: StateOK, Findings: 2}) {
		t.Fatalf("unexpected status %+v", statuses[0])
	}
	if statuses[1].State != StateFailed || statuses[1].Reason != "boom" {
		t.Fatalf("unexpected status %+v", statuses[1])
	}
}

func TestIgnored(t *testing.T) {
	cases := []struct {
		value string
		check string
		want  bool
	}{
		{"true", "x", true},
		{"all", "x", true},
		{"x", "x", true},
		{"a, x ,b", "x", true},
		{"y", "x", false},
		{"", "x", false},
		{"false", "x", false},
	}
	for _, c := range cases {
		meta := &metav1.ObjectMeta{Annotations: map[string]string{IgnoreAnnotation: c.value}}
		if got := ignored(meta, c.check); got != c.want {
			t.Errorf("ignored(%q, %q) = %v, want %v", c.value, c.check, got, c.want)
		}
	}
	if ignored(&metav1.ObjectMeta{}, "x") {
		t.Error("no annotation must not ignore")
	}
}

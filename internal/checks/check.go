// Package checks holds the anomaly detectors and the engine that runs them
// over a cluster.Snapshot.
package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// Severity classifies how bad a finding is.
type Severity string

const (
	// SeverityCritical means user-facing traffic is broken or cluster
	// operations are blocked right now.
	SeverityCritical Severity = "CRITICAL"
	// SeverityWarning means a latent risk or degraded state that needs
	// attention.
	SeverityWarning Severity = "WARNING"
	// SeverityInfo is a hygiene-level observation.
	SeverityInfo Severity = "INFO"
)

// Rank returns a comparable weight (higher is worse).
func (s Severity) Rank() int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}

// Ref points at a Kubernetes object related to a finding (the Service an
// Ingress references, the pod an endpoint targets, ...).
type Ref struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// String renders the reference as Kind/Name or Kind/Namespace/Name.
func (r Ref) String() string {
	if r.Namespace != "" {
		return r.Kind + "/" + r.Namespace + "/" + r.Name
	}
	return r.Kind + "/" + r.Name
}

// Finding is a single anomaly detected by a check. The JSON shape is the
// contract consumed by every renderer and by external tools.
type Finding struct {
	Check     string   `json:"check"`
	Severity  Severity `json:"severity"`
	Namespace string   `json:"namespace,omitempty"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Related   []Ref    `json:"related,omitempty"`
	Message   string   `json:"message"`
	Hint      string   `json:"hint,omitempty"`
	// Fingerprint identifies the anomaly stably across scans. It hashes the
	// check, the object, the related objects and Key, but not the message,
	// which may embed ages or dates that change on every run. Consumers can
	// use it to deduplicate, diff two reports or suppress known findings.
	Fingerprint string `json:"fingerprint"`
	// Key disambiguates several findings on the same object (an endpoint
	// address, a webhook name, an ingress path). Not rendered; it only
	// feeds Fingerprint.
	Key string `json:"-"`
}

// Object returns the human form "Kind/Name".
func (f Finding) Object() string { return f.Kind + "/" + f.Name }

// Fingerprint computes the stable identifier of a finding.
func Fingerprint(f Finding) string {
	parts := []string{f.Check, f.Namespace, f.Kind, f.Name, f.Key}
	for _, r := range f.Related {
		parts = append(parts, r.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// Options carries user-tunable thresholds shared by checks.
type Options struct {
	CertWarnDays   int
	StuckThreshold time.Duration
	// Now is the reference time for ages and expiries. The zero value means
	// time.Now(); tests set it to get deterministic results.
	Now time.Time
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// Check is the contract every anomaly detector implements. Checks are pure
// functions over the Snapshot: they never talk to the API server, which
// keeps them fast, deterministic and unit-testable with plain structs.
type Check interface {
	Name() string
	Description() string
	// Needs lists the snapshot resources the check reads. The engine fetches
	// only what the selected checks need, and skips a check (with a notice)
	// when a required resource could not be listed, instead of running it on
	// empty data and reporting every object as broken.
	Needs() []cluster.Need
	Run(ctx context.Context, snap *cluster.Snapshot, opts Options) ([]Finding, error)
}

// IgnoreAnnotation lets an object opt out of findings. "true" or "all"
// silences every check for that object; a comma-separated list of check
// names silences only those. A radar with no mute button gets turned off.
const IgnoreAnnotation = "oskar.io/ignore"

func ignored(obj metav1.Object, check string) bool {
	v, ok := obj.GetAnnotations()[IgnoreAnnotation]
	if !ok {
		return false
	}
	for _, part := range strings.Split(v, ",") {
		switch strings.TrimSpace(part) {
		case "true", "all", check:
			return true
		}
	}
	return false
}

// All returns every registered check, in display order.
func All() []Check {
	return []Check{
		&GhostEndpoints{},
		&DeadWebhook{},
		&BrokenIngress{},
		&ServiceSelector{},
		&CertExpiry{},
		&StuckTerminating{},
		&ReleasedPV{},
		&OrphanedHPA{},
		&OpenShiftRoute{},
	}
}

// Select applies --checks / --skip-checks filtering, validates the names
// and drops duplicates.
func Select(only, skip []string) ([]Check, error) {
	all := All()
	byName := make(map[string]Check, len(all))
	for _, c := range all {
		byName[c.Name()] = c
	}
	validate := func(names []string) error {
		for _, n := range names {
			if _, ok := byName[n]; !ok {
				return fmt.Errorf("unknown check %q (run 'oskar checks' to list available checks)", n)
			}
		}
		return nil
	}
	if err := validate(only); err != nil {
		return nil, err
	}
	if err := validate(skip); err != nil {
		return nil, err
	}
	skipSet := make(map[string]bool, len(skip))
	for _, n := range skip {
		skipSet[n] = true
	}
	var out []Check
	seen := make(map[string]bool)
	add := func(c Check) {
		if !skipSet[c.Name()] && !seen[c.Name()] {
			seen[c.Name()] = true
			out = append(out, c)
		}
	}
	if len(only) > 0 {
		for _, n := range only {
			add(byName[n])
		}
		return out, nil
	}
	for _, c := range all {
		add(c)
	}
	return out, nil
}

// Needed merges the needs of several checks: a resource is listed
// cluster-wide if any check asks for it, and is required if any check
// requires it.
func Needed(cs []Check) []cluster.Need {
	merged := make(map[cluster.Resource]cluster.Need)
	var order []cluster.Resource
	for _, c := range cs {
		for _, n := range c.Needs() {
			m, ok := merged[n.Resource]
			if !ok {
				order = append(order, n.Resource)
				m = cluster.Need{Resource: n.Resource, Optional: true}
			}
			m.ClusterWide = m.ClusterWide || n.ClusterWide
			m.Optional = m.Optional && n.Optional
			merged[n.Resource] = m
		}
	}
	out := make([]cluster.Need, 0, len(order))
	for _, r := range order {
		out = append(out, merged[r])
	}
	return out
}

// Status reports how a single check fared in a scan.
type Status struct {
	Name     string `json:"name"`
	State    string `json:"state"` // ok, skipped or failed
	Findings int    `json:"findings"`
	Reason   string `json:"reason,omitempty"`
}

// Check states.
const (
	StateOK      = "ok"
	StateSkipped = "skipped"
	StateFailed  = "failed"
)

// RunAll executes the checks against the snapshot. A check whose required
// resources the snapshot could not list is skipped rather than run on
// partial data. Fingerprints are filled in and findings are sorted, most
// severe first.
func RunAll(ctx context.Context, cs []Check, snap *cluster.Snapshot, opts Options) ([]Finding, []Status) {
	var all []Finding
	statuses := make([]Status, 0, len(cs))
	for _, c := range cs {
		st := Status{Name: c.Name()}
		if reason := snap.Unmet(c.Needs()); reason != "" {
			st.State, st.Reason = StateSkipped, reason
			statuses = append(statuses, st)
			continue
		}
		fs, err := c.Run(ctx, snap, opts)
		if err != nil {
			st.State, st.Reason = StateFailed, err.Error()
			statuses = append(statuses, st)
			continue
		}
		for i := range fs {
			fs[i].Fingerprint = Fingerprint(fs[i])
		}
		st.State, st.Findings = StateOK, len(fs)
		statuses = append(statuses, st)
		all = append(all, fs...)
	}
	Sort(all)
	return all, statuses
}

// Sort orders findings by severity (worst first), then namespace, kind,
// name, check and key, so reports are stable between runs.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Key < b.Key
	})
}

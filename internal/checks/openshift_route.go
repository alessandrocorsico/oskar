package checks

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// OpenShiftRoute detects Routes (route.openshift.io/v1) whose spec.to or
// alternateBackends reference a Service that does not exist. The check runs
// only when the cluster actually serves the Route API — on EKS/AKS/GKE/RKE2
// it is a silent no-op, keeping OSKAR distribution-agnostic.
type OpenShiftRoute struct{}

// Name implements Check.
func (c *OpenShiftRoute) Name() string { return "openshift-route" }

// Description implements Check.
func (c *OpenShiftRoute) Description() string {
	return "OpenShift Routes referencing missing Services (auto-skipped off OpenShift)"
}

// Needs implements Check.
func (c *OpenShiftRoute) Needs() []cluster.Need {
	return cluster.Needs(cluster.ResourceRoutes, cluster.ResourceServices)
}

// Run implements Check.
func (c *OpenShiftRoute) Run(_ context.Context, snap *cluster.Snapshot, _ Options) ([]Finding, error) {
	if !snap.HasRoutes {
		return nil, nil
	}
	var out []Finding
	for i := range snap.Routes {
		r := &snap.Routes[i]
		if ignored(r, c.Name()) {
			continue
		}
		ns := r.GetNamespace()
		name := r.GetName()
		check := func(kind, svcName, where string) {
			if svcName == "" || (kind != "" && kind != "Service") {
				return
			}
			if snap.GetService(ns, svcName) == nil {
				out = append(out, Finding{
					Check:     c.Name(),
					Severity:  SeverityCritical,
					Namespace: ns,
					Kind:      "Route",
					Name:      name,
					Related:   []Ref{{Kind: "Service", Namespace: ns, Name: svcName}},
					Key:       where + ":" + svcName,
					Message:   fmt.Sprintf("%s references Service %q which does not exist — the route serves 503s", where, svcName),
					Hint:      "fix spec.to / spec.alternateBackends or restore the Service",
				})
			}
		}
		toKind, _, _ := unstructured.NestedString(r.Object, "spec", "to", "kind")
		toName, _, _ := unstructured.NestedString(r.Object, "spec", "to", "name")
		check(toKind, toName, "spec.to")
		alts, found, _ := unstructured.NestedSlice(r.Object, "spec", "alternateBackends")
		if found {
			for _, a := range alts {
				m, ok := a.(map[string]interface{})
				if !ok {
					continue
				}
				k, _ := m["kind"].(string)
				n, _ := m["name"].(string)
				check(k, n, "spec.alternateBackends")
			}
		}
	}
	return out, nil
}

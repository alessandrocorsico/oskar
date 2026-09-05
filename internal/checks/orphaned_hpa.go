package checks

import (
	"context"
	"fmt"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// OrphanedHPA detects HorizontalPodAutoscalers whose scaleTargetRef points
// at a workload that no longer exists. The HPA sits there logging errors and
// autoscaling nothing — easy to miss after a rename or a migration.
type OrphanedHPA struct{}

// Name implements Check.
func (c *OrphanedHPA) Name() string { return "orphaned-hpa" }

// Description implements Check.
func (c *OrphanedHPA) Description() string {
	return "HorizontalPodAutoscalers whose scale target no longer exists"
}

// Needs implements Check. Workloads are fetched as metadata only. ReplicaSet
// targets are rare, so that list is optional: without it they are simply
// not verified.
func (c *OrphanedHPA) Needs() []cluster.Need {
	return []cluster.Need{
		{Resource: cluster.ResourceHPAs},
		{Resource: cluster.ResourceDeployments},
		{Resource: cluster.ResourceStatefulSets},
		{Resource: cluster.ResourceReplicaSets, Optional: true},
	}
}

// Run implements Check.
func (c *OrphanedHPA) Run(_ context.Context, snap *cluster.Snapshot, _ Options) ([]Finding, error) {
	var out []Finding
	for i := range snap.HPAs {
		hpa := &snap.HPAs[i]
		if ignored(hpa, c.Name()) {
			continue
		}
		ref := hpa.Spec.ScaleTargetRef
		// Only the built-in apps/v1 kinds can be verified from the snapshot.
		// Custom scale targets (Argo Rollouts, CRDs with a /scale subresource)
		// or a same-named kind in another group are skipped.
		if ref.APIVersion != "" && ref.APIVersion != "apps/v1" {
			continue
		}
		switch ref.Kind {
		case "Deployment", "StatefulSet":
		case "ReplicaSet":
			if !snap.Available(cluster.ResourceReplicaSets) {
				continue
			}
		default:
			continue
		}
		if snap.HasWorkload(ref.Kind, hpa.Namespace, ref.Name) {
			continue
		}
		out = append(out, Finding{
			Check:     c.Name(),
			Severity:  SeverityWarning,
			Namespace: hpa.Namespace,
			Kind:      "HorizontalPodAutoscaler",
			Name:      hpa.Name,
			Related:   []Ref{{Kind: ref.Kind, Namespace: hpa.Namespace, Name: ref.Name}},
			Message:   fmt.Sprintf("scale target %s %q does not exist — the HPA is inert and only logs errors", ref.Kind, ref.Name),
			Hint:      "the workload was deleted or renamed without updating the HPA; delete the HPA or fix scaleTargetRef",
		})
	}
	return out, nil
}

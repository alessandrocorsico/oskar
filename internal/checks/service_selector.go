package checks

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// ServiceSelector detects Services whose label selector matches zero pods.
// Traffic sent to such a Service goes nowhere, but the Service itself looks
// fine in `kubectl get svc`. Selector-less Services (manual endpoints,
// headless patterns) and ExternalName Services are intentionally skipped.
//
// Workloads that scale to zero on purpose (KEDA, Knative) trip this check by
// design: annotate their Services with oskar.io/ignore=service-selector.
type ServiceSelector struct{}

// Name implements Check.
func (c *ServiceSelector) Name() string { return "service-selector" }

// Description implements Check.
func (c *ServiceSelector) Description() string {
	return "Services whose selector matches no pods (traffic goes nowhere)"
}

// Needs implements Check.
func (c *ServiceSelector) Needs() []cluster.Need {
	return cluster.Needs(cluster.ResourceServices, cluster.ResourcePods)
}

// Run implements Check.
func (c *ServiceSelector) Run(_ context.Context, snap *cluster.Snapshot, _ Options) ([]Finding, error) {
	var out []Finding
	for i := range snap.Services {
		svc := &snap.Services[i]
		// Services may have been listed cluster-wide for another check while
		// pods were restricted to --namespace: stay within the pods we have.
		if snap.Namespace != "" && svc.Namespace != snap.Namespace {
			continue
		}
		if len(svc.Spec.Selector) == 0 || svc.Spec.Type == corev1.ServiceTypeExternalName || ignored(svc, c.Name()) {
			continue
		}
		sel := labels.SelectorFromSet(labels.Set(svc.Spec.Selector))
		matched := false
		for _, p := range snap.PodsInNamespace(svc.Namespace) {
			if sel.Matches(labels.Set(p.Labels)) {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, Finding{
				Check:     c.Name(),
				Severity:  SeverityWarning,
				Namespace: svc.Namespace,
				Kind:      "Service",
				Name:      svc.Name,
				Message:   "selector matches no pods — traffic to this Service goes nowhere",
				Hint:      "check for label typos or a missing/scaled-to-zero workload: kubectl get pods -n " + svc.Namespace + " -l " + sel.String(),
			})
		}
	}
	return out, nil
}

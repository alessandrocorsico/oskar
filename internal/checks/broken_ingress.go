package checks

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// BrokenIngress detects Ingress backends that reference a Service which does
// not exist, or a port the Service does not expose. Both cases turn into
// 502/503 for users while the Ingress object itself reports no error.
type BrokenIngress struct{}

// Name implements Check.
func (c *BrokenIngress) Name() string { return "broken-ingress" }

// Description implements Check.
func (c *BrokenIngress) Description() string {
	return "Ingress backends referencing missing Services or ports (requests 502/503)"
}

// Needs implements Check.
func (c *BrokenIngress) Needs() []cluster.Need {
	return cluster.Needs(cluster.ResourceIngresses, cluster.ResourceServices)
}

type ingressBackendRef struct {
	where string
	svc   *networkingv1.IngressServiceBackend
}

// Run implements Check.
func (c *BrokenIngress) Run(_ context.Context, snap *cluster.Snapshot, _ Options) ([]Finding, error) {
	var out []Finding
	for i := range snap.Ingresses {
		ing := &snap.Ingresses[i]
		if ignored(ing, c.Name()) {
			continue
		}
		var refs []ingressBackendRef
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			refs = append(refs, ingressBackendRef{"default backend", ing.Spec.DefaultBackend.Service})
		}
		for ri := range ing.Spec.Rules {
			rule := &ing.Spec.Rules[ri]
			if rule.HTTP == nil {
				continue
			}
			for pi := range rule.HTTP.Paths {
				p := &rule.HTTP.Paths[pi]
				// Resource-typed backends (e.g. object storage buckets) are
				// out of scope.
				if p.Backend.Service == nil {
					continue
				}
				refs = append(refs, ingressBackendRef{fmt.Sprintf("rule %q path %q", rule.Host, p.Path), p.Backend.Service})
			}
		}
		for _, r := range refs {
			f := Finding{
				Check:     c.Name(),
				Severity:  SeverityCritical,
				Namespace: ing.Namespace,
				Kind:      "Ingress",
				Name:      ing.Name,
				Related:   []Ref{{Kind: "Service", Namespace: ing.Namespace, Name: r.svc.Name}},
				Key:       r.where,
			}
			svc := snap.GetService(ing.Namespace, r.svc.Name)
			switch {
			case svc == nil:
				f.Message = fmt.Sprintf("%s references Service %q which does not exist — requests will 503", r.where, r.svc.Name)
				f.Hint = "the Service was probably renamed or deleted; fix the Ingress backend or restore the Service"
			case !servicePortExists(svc, r.svc.Port):
				f.Message = fmt.Sprintf("%s references port %s on Service %q, but the Service does not expose it", r.where, portString(r.svc.Port), r.svc.Name)
				f.Hint = "compare the Ingress backend port with the Service's spec.ports — name and number must match exactly"
			default:
				continue
			}
			out = append(out, f)
		}
	}
	return out, nil
}

func servicePortExists(svc *corev1.Service, p networkingv1.ServiceBackendPort) bool {
	// Nothing specified: nothing to validate.
	if p.Name == "" && p.Number == 0 {
		return true
	}
	for _, sp := range svc.Spec.Ports {
		if p.Name != "" && sp.Name == p.Name {
			return true
		}
		if p.Number != 0 && sp.Port == p.Number {
			return true
		}
	}
	return false
}

func portString(p networkingv1.ServiceBackendPort) string {
	if p.Name != "" {
		return fmt.Sprintf("%q (by name)", p.Name)
	}
	return fmt.Sprintf("%d", p.Number)
}

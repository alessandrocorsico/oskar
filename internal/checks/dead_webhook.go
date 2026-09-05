package checks

import (
	"context"
	"fmt"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// DeadWebhook detects Validating/MutatingWebhookConfigurations whose backend
// Service is missing or has no ready endpoints.
//
// With failurePolicy=Fail (the default), a dead webhook makes the API server
// reject every matching request — deploys fail, nodes can't drain, sometimes
// pods can't even be created — while the webhook object itself looks
// perfectly healthy. It is one of the most common "the whole cluster is
// broken and nothing says why" scenarios.
type DeadWebhook struct{}

// Name implements Check.
func (c *DeadWebhook) Name() string { return "dead-webhook" }

// Description implements Check.
func (c *DeadWebhook) Description() string {
	return "Admission webhooks whose backend Service is missing or has no ready endpoints"
}

// Needs implements Check. Webhook configurations are cluster-scoped and
// reference Services anywhere, so Services and EndpointSlices must be
// listed cluster-wide even when the scan is restricted to one namespace.
func (c *DeadWebhook) Needs() []cluster.Need {
	return []cluster.Need{
		{Resource: cluster.ResourceValidatingWebhooks},
		{Resource: cluster.ResourceMutatingWebhooks},
		{Resource: cluster.ResourceServices, ClusterWide: true},
		{Resource: cluster.ResourceEndpointSlices, ClusterWide: true},
	}
}

// Run implements Check.
func (c *DeadWebhook) Run(_ context.Context, snap *cluster.Snapshot, _ Options) ([]Finding, error) {
	var out []Finding
	for i := range snap.ValidatingWebhooks {
		cfg := &snap.ValidatingWebhooks[i]
		if ignored(cfg, c.Name()) {
			continue
		}
		for wi := range cfg.Webhooks {
			w := &cfg.Webhooks[wi]
			if f := c.eval(snap, "ValidatingWebhookConfiguration", &cfg.ObjectMeta, w.Name, w.ClientConfig, w.FailurePolicy); f != nil {
				out = append(out, *f)
			}
		}
	}
	for i := range snap.MutatingWebhooks {
		cfg := &snap.MutatingWebhooks[i]
		if ignored(cfg, c.Name()) {
			continue
		}
		for wi := range cfg.Webhooks {
			w := &cfg.Webhooks[wi]
			if f := c.eval(snap, "MutatingWebhookConfiguration", &cfg.ObjectMeta, w.Name, w.ClientConfig, w.FailurePolicy); f != nil {
				out = append(out, *f)
			}
		}
	}
	return out, nil
}

func (c *DeadWebhook) eval(snap *cluster.Snapshot, kind string, meta *metav1.ObjectMeta, webhookName string, cc admissionv1.WebhookClientConfig, fp *admissionv1.FailurePolicyType) *Finding {
	// URL-based webhooks point outside the cluster; nothing to verify from a
	// snapshot.
	if cc.Service == nil {
		return nil
	}
	sev := SeverityCritical
	policy := "Fail"
	if fp != nil && *fp == admissionv1.Ignore {
		sev = SeverityWarning
		policy = "Ignore"
	}
	svc := cc.Service
	f := Finding{
		Check:    c.Name(),
		Severity: sev,
		Kind:     kind,
		Name:     meta.Name,
		Related:  []Ref{{Kind: "Service", Namespace: svc.Namespace, Name: svc.Name}},
		Key:      webhookName,
	}
	switch {
	case snap.GetService(svc.Namespace, svc.Name) == nil:
		f.Message = fmt.Sprintf("webhook %q points at Service %s/%s which does not exist (failurePolicy=%s)",
			webhookName, svc.Namespace, svc.Name, policy)
		f.Hint = "with failurePolicy=Fail the API server rejects every matching request until this is fixed — the classic invisible cluster-wide outage; fix the reference or remove the webhook configuration"
	case !snap.ServiceHasReadyEndpoints(svc.Namespace, svc.Name):
		f.Message = fmt.Sprintf("webhook %q backend Service %s/%s has no ready endpoints (failurePolicy=%s)",
			webhookName, svc.Namespace, svc.Name, policy)
		f.Hint = "the webhook pods are down or not Ready; matching API requests will fail (Fail) or silently skip admission (Ignore) until the backend recovers"
	default:
		return nil
	}
	return &f
}

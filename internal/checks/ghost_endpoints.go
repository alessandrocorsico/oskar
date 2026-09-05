package checks

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// endpointSliceController is the managed-by label value of the slices
// written by kube-controller-manager's EndpointSlice controller.
const endpointSliceController = "endpointslice-controller.k8s.io"

// GhostEndpoints detects ready EndpointSlice entries that point at pods
// which no longer exist, or whose addresses do not match the target pod's
// actual IPs.
//
// This is the classic "ghost endpoint" failure mode: kube-proxy keeps
// routing a share of the Service traffic to an IP nobody owns anymore, so a
// fraction of requests silently time out or hit huge TTFB, while every
// dashboard stays green. Nothing in `kubectl get` surfaces it.
type GhostEndpoints struct{}

// Name implements Check.
func (c *GhostEndpoints) Name() string { return "ghost-endpoints" }

// Description implements Check.
func (c *GhostEndpoints) Description() string {
	return "EndpointSlice entries pointing at dead pods or mismatched IPs (silent traffic blackholes)"
}

// Needs implements Check.
func (c *GhostEndpoints) Needs() []cluster.Need {
	return []cluster.Need{
		{Resource: cluster.ResourceEndpointSlices},
		{Resource: cluster.ResourcePods},
		{Resource: cluster.ResourceServices, Optional: true}, // only read for the ignore annotation
	}
}

// Run implements Check.
func (c *GhostEndpoints) Run(_ context.Context, snap *cluster.Snapshot, _ Options) ([]Finding, error) {
	var out []Finding
	for i := range snap.EndpointSlices {
		es := &snap.EndpointSlices[i]
		// Slices may have been listed cluster-wide for another check while
		// pods were restricted to --namespace: stay within the pods we have.
		if snap.Namespace != "" && es.Namespace != snap.Namespace {
			continue
		}
		if es.AddressType != discoveryv1.AddressTypeIPv4 && es.AddressType != discoveryv1.AddressTypeIPv6 {
			continue
		}
		// Only slices written by the core EndpointSlice controller: mirrored
		// slices (hand-written Endpoints) and slices owned by other controllers
		// reference pods on their own terms.
		if es.Labels[discoveryv1.LabelManagedBy] != endpointSliceController {
			continue
		}
		svcName := es.Labels[discoveryv1.LabelServiceName]
		if svcName == "" || ignored(es, c.Name()) {
			continue
		}
		if svc := snap.GetService(es.Namespace, svcName); svc != nil && ignored(svc, c.Name()) {
			continue
		}
		related := []Ref{{Kind: "Service", Namespace: es.Namespace, Name: svcName}}
		for _, ep := range es.Endpoints {
			ref := ep.TargetRef
			if ref == nil || ref.Kind != "Pod" || len(ep.Addresses) == 0 {
				continue
			}
			// kube-proxy only forwards to ready endpoints. A not-ready entry
			// (a pod starting up or terminating) cannot blackhole traffic, and
			// it is exactly what a rollout looks like between two API calls.
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			ns := ref.Namespace
			if ns == "" {
				ns = es.Namespace
			}
			addr := strings.Join(ep.Addresses, ", ")
			pod := snap.GetPod(ns, ref.Name)
			if pod == nil {
				out = append(out, Finding{
					Check:     c.Name(),
					Severity:  SeverityCritical,
					Namespace: es.Namespace,
					Kind:      "EndpointSlice",
					Name:      es.Name,
					Related:   related,
					Key:       addr,
					Message: fmt.Sprintf("ready endpoint %s targets pod %q which no longer exists — stale entry blackholing a share of the Service traffic",
						addr, ref.Name),
					Hint: "the endpoint controller lost sync; check kube-controller-manager health, then force a resync (e.g. re-apply the Service) or delete the stale EndpointSlice",
				})
				continue
			}
			ips := podIPs(pod)
			// A pod without an assigned IP (Pending) or being deleted can be
			// transiently out of sync with its slice; skip to avoid noise.
			if len(ips) == 0 || pod.DeletionTimestamp != nil {
				continue
			}
			if !matchesAny(ep.Addresses, ips) {
				out = append(out, Finding{
					Check:     c.Name(),
					Severity:  SeverityCritical,
					Namespace: es.Namespace,
					Kind:      "EndpointSlice",
					Name:      es.Name,
					Related:   related,
					Key:       addr,
					Message: fmt.Sprintf("endpoint address %s does not match any current IP of pod %q (pod IPs: %s) — ghost IP",
						addr, pod.Name, strings.Join(ips, ", ")),
					Hint: "traffic for this Service is routed to an address the pod no longer owns; delete the stale EndpointSlice and verify the endpoint controller resyncs",
				})
			}
		}
	}
	return out, nil
}

func matchesAny(addrs, ips []string) bool {
	for _, a := range addrs {
		for _, ip := range ips {
			if a == ip {
				return true
			}
		}
	}
	return false
}

func podIPs(p *corev1.Pod) []string {
	var ips []string
	if p.Status.PodIP != "" {
		ips = append(ips, p.Status.PodIP)
	}
	for _, pi := range p.Status.PodIPs {
		if pi.IP != "" && pi.IP != p.Status.PodIP {
			ips = append(ips, pi.IP)
		}
	}
	return ips
}

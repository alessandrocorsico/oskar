package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// StuckTerminating detects pods, namespaces and PVCs that have been in
// Terminating state longer than a threshold — almost always a finalizer
// whose controller is gone, an unreachable node, or (for namespaces) an
// unavailable aggregated APIService.
type StuckTerminating struct{}

// Name implements Check.
func (c *StuckTerminating) Name() string { return "stuck-terminating" }

// Description implements Check.
func (c *StuckTerminating) Description() string {
	return "Pods, Namespaces and PVCs stuck in Terminating beyond the threshold"
}

// Needs implements Check. Namespaces are optional so that a user who can
// only list inside one namespace still gets the pod and PVC part.
func (c *StuckTerminating) Needs() []cluster.Need {
	return []cluster.Need{
		{Resource: cluster.ResourcePods},
		{Resource: cluster.ResourcePersistentVolumeClaims},
		{Resource: cluster.ResourceNamespaces, Optional: true},
	}
}

// Run implements Check.
func (c *StuckTerminating) Run(_ context.Context, snap *cluster.Snapshot, opts Options) ([]Finding, error) {
	th := opts.StuckThreshold
	now := opts.now()
	var out []Finding

	for i := range snap.Pods {
		p := &snap.Pods[i]
		if p.DeletionTimestamp == nil || ignored(p, c.Name()) {
			continue
		}
		age := now.Sub(p.DeletionTimestamp.Time)
		if age <= th {
			continue
		}
		msg := fmt.Sprintf("stuck in Terminating for %s", humanDur(age))
		if len(p.Finalizers) > 0 {
			msg += fmt.Sprintf(" (finalizers: %s)", strings.Join(p.Finalizers, ", "))
		}
		out = append(out, Finding{
			Check:     c.Name(),
			Severity:  SeverityWarning,
			Namespace: p.Namespace,
			Kind:      "Pod",
			Name:      p.Name,
			Message:   msg,
			Hint:      "usual suspects: a finalizer whose controller is gone, or an unreachable node; investigate before force-deleting (--force --grace-period=0 can orphan resources)",
		})
	}

	if snap.Available(cluster.ResourceNamespaces) {
		for i := range snap.Namespaces {
			ns := &snap.Namespaces[i]
			if ns.Status.Phase != corev1.NamespaceTerminating || ns.DeletionTimestamp == nil || ignored(ns, c.Name()) {
				continue
			}
			age := now.Sub(ns.DeletionTimestamp.Time)
			if age <= th {
				continue
			}
			out = append(out, Finding{
				Check:    c.Name(),
				Severity: SeverityWarning,
				Kind:     "Namespace",
				Name:     ns.Name,
				Message:  fmt.Sprintf("stuck in Terminating for %s", humanDur(age)),
				Hint:     "usually an unavailable aggregated APIService or leftover finalizers: run 'kubectl get apiservice' and look for Available=False, then inspect the namespace's status.conditions",
			})
		}
	}

	for i := range snap.PersistentVolumeClaims {
		pvc := &snap.PersistentVolumeClaims[i]
		if pvc.DeletionTimestamp == nil || ignored(pvc, c.Name()) {
			continue
		}
		age := now.Sub(pvc.DeletionTimestamp.Time)
		if age <= th {
			continue
		}
		out = append(out, Finding{
			Check:     c.Name(),
			Severity:  SeverityWarning,
			Namespace: pvc.Namespace,
			Kind:      "PersistentVolumeClaim",
			Name:      pvc.Name,
			Message:   fmt.Sprintf("stuck in Terminating for %s", humanDur(age)),
			Hint:      "the kubernetes.io/pvc-protection finalizer keeps a PVC Terminating while a pod still mounts it — find that pod, or investigate the CSI driver",
		})
	}

	return out, nil
}

func humanDur(d time.Duration) string {
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	mins := (d % time.Hour) / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

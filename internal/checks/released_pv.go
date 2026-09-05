package checks

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// ReleasedPV detects PersistentVolumes stuck in Released or Failed phase.
// Released volumes still hold data and cost money, but cannot be re-bound;
// Failed volumes mean automatic reclamation broke and needs a human.
type ReleasedPV struct{}

// Name implements Check.
func (c *ReleasedPV) Name() string { return "released-pv" }

// Description implements Check.
func (c *ReleasedPV) Description() string {
	return "PersistentVolumes in Released or Failed phase (unusable but still allocated)"
}

// Needs implements Check.
func (c *ReleasedPV) Needs() []cluster.Need {
	return cluster.Needs(cluster.ResourcePersistentVolumes)
}

// Run implements Check.
func (c *ReleasedPV) Run(_ context.Context, snap *cluster.Snapshot, _ Options) ([]Finding, error) {
	var out []Finding
	for i := range snap.PersistentVolumes {
		pv := &snap.PersistentVolumes[i]
		if ignored(pv, c.Name()) {
			continue
		}
		f := Finding{
			Check: c.Name(),
			Kind:  "PersistentVolume",
			Name:  pv.Name,
		}
		claim := ""
		if pv.Spec.ClaimRef != nil {
			claim = fmt.Sprintf(" (last claim: %s/%s)", pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
			f.Related = []Ref{{Kind: "PersistentVolumeClaim", Namespace: pv.Spec.ClaimRef.Namespace, Name: pv.Spec.ClaimRef.Name}}
		}
		switch pv.Status.Phase {
		case corev1.VolumeReleased:
			f.Severity = SeverityWarning
			f.Message = fmt.Sprintf("PV is Released%s — the claim is gone but the volume (and its data) is still allocated and cannot be re-bound", claim)
			f.Hint = "decide the fate of the data, then either delete the PV or clear spec.claimRef to make it Available again; also review the reclaimPolicy"
		case corev1.VolumeFailed:
			f.Severity = SeverityCritical
			f.Message = fmt.Sprintf("PV is in Failed state — automatic reclamation failed%s", claim)
			f.Hint = "inspect the PV events and the storage backend; manual cleanup is usually required"
		default:
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

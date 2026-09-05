package checks

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func testPV(name string, phase corev1.PersistentVolumePhase, claimNS, claimName string) corev1.PersistentVolume {
	pv := corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.PersistentVolumeStatus{Phase: phase},
	}
	if claimName != "" {
		pv.Spec.ClaimRef = &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: claimNS, Name: claimName}
	}
	return pv
}

func runPV(t *testing.T, pvs ...corev1.PersistentVolume) []Finding {
	t.Helper()
	fs, err := (&ReleasedPV{}).Run(context.Background(), &cluster.Snapshot{PersistentVolumes: pvs}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestReleasedPVReleased(t *testing.T) {
	fs := runPV(t, testPV("pv-1", corev1.VolumeReleased, "db", "data-0"))
	if len(fs) != 1 || fs[0].Severity != SeverityWarning || fs[0].Kind != "PersistentVolume" {
		t.Fatalf("expected 1 WARNING finding, got %+v", fs)
	}
	want := Ref{Kind: "PersistentVolumeClaim", Namespace: "db", Name: "data-0"}
	if len(fs[0].Related) != 1 || fs[0].Related[0] != want {
		t.Fatalf("expected related %v, got %v", want, fs[0].Related)
	}
}

func TestReleasedPVFailed(t *testing.T) {
	fs := runPV(t, testPV("pv-2", corev1.VolumeFailed, "", ""))
	if len(fs) != 1 || fs[0].Severity != SeverityCritical || len(fs[0].Related) != 0 {
		t.Fatalf("expected 1 CRITICAL finding without related refs, got %+v", fs)
	}
}

func TestReleasedPVHealthyPhases(t *testing.T) {
	fs := runPV(t,
		testPV("pv-3", corev1.VolumeBound, "db", "data-1"),
		testPV("pv-4", corev1.VolumeAvailable, "", ""),
		testPV("pv-5", corev1.VolumePending, "", ""),
	)
	if len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %+v", fs)
	}
}

func TestReleasedPVIgnoreAnnotation(t *testing.T) {
	pv := testPV("pv-1", corev1.VolumeReleased, "db", "data-0")
	annotateIgnore(&pv.ObjectMeta, "released-pv")
	if fs := runPV(t, pv); len(fs) != 0 {
		t.Fatalf("annotated PV must be skipped, got %+v", fs)
	}
}

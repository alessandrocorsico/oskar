package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

var stuckNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func deletedAt(age time.Duration) *metav1.Time {
	t := metav1.NewTime(stuckNow.Add(-age))
	return &t
}

func runStuck(t *testing.T, snap *cluster.Snapshot) []Finding {
	t.Helper()
	fs, err := (&StuckTerminating{}).Run(context.Background(), snap, Options{StuckThreshold: time.Hour, Now: stuckNow})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestStuckTerminatingPod(t *testing.T) {
	pod := testPod("default", "worker-1", nil)
	pod.DeletionTimestamp = deletedAt(2*time.Hour + 5*time.Minute)
	pod.Finalizers = []string{"example.com/cleanup"}
	fs := runStuck(t, &cluster.Snapshot{Pods: []corev1.Pod{pod}})
	if len(fs) != 1 || fs[0].Severity != SeverityWarning || fs[0].Kind != "Pod" {
		t.Fatalf("expected 1 WARNING pod finding, got %+v", fs)
	}
	if !strings.Contains(fs[0].Message, "2h5m") || !strings.Contains(fs[0].Message, "example.com/cleanup") {
		t.Fatalf("message should carry age and finalizers, got %q", fs[0].Message)
	}
}

func TestStuckTerminatingBelowThreshold(t *testing.T) {
	pod := testPod("default", "worker-1", nil)
	pod.DeletionTimestamp = deletedAt(10 * time.Minute)
	if fs := runStuck(t, &cluster.Snapshot{Pods: []corev1.Pod{pod}}); len(fs) != 0 {
		t.Fatalf("a pod terminating for 10m is normal, got %+v", fs)
	}
}

func TestStuckTerminatingNamespaceAndPVC(t *testing.T) {
	ns := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "old-project", DeletionTimestamp: deletedAt(3 * time.Hour)},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	}
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", DeletionTimestamp: deletedAt(26 * time.Hour)},
	}
	fs := runStuck(t, &cluster.Snapshot{Namespaces: []corev1.Namespace{ns}, PersistentVolumeClaims: []corev1.PersistentVolumeClaim{pvc}})
	if len(fs) != 2 {
		t.Fatalf("expected 2 findings, got %+v", fs)
	}
	kinds := map[string]bool{}
	for _, f := range fs {
		kinds[f.Kind] = true
	}
	if !kinds["Namespace"] || !kinds["PersistentVolumeClaim"] {
		t.Fatalf("expected a Namespace and a PVC finding, got %+v", fs)
	}
}

func TestStuckTerminatingIgnoreAnnotation(t *testing.T) {
	pod := testPod("default", "worker-1", nil)
	pod.DeletionTimestamp = deletedAt(2 * time.Hour)
	annotateIgnore(&pod.ObjectMeta, "stuck-terminating")
	if fs := runStuck(t, &cluster.Snapshot{Pods: []corev1.Pod{pod}}); len(fs) != 0 {
		t.Fatalf("annotated pod must be skipped, got %+v", fs)
	}
}

func TestHumanDur(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Minute:                 "5m",
		2*time.Hour + 5*time.Minute:     "2h5m",
		26 * time.Hour:                  "1d2h",
		3*24*time.Hour + 30*time.Second: "3d0h",
		90*time.Minute + 29*time.Second: "1h30m",
		90*time.Minute + 31*time.Second: "1h31m",
	}
	for d, want := range cases {
		if got := humanDur(d); got != want {
			t.Errorf("humanDur(%s) = %q, want %q", d, got, want)
		}
	}
}

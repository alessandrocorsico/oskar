package checks

import (
	"context"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func testHPA(ns, name, apiVersion, kind, target string) autoscalingv2.HorizontalPodAutoscaler {
	return autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: apiVersion, Kind: kind, Name: target},
		},
	}
}

func workloadMeta(ns, name string) metav1.PartialObjectMetadata {
	return metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func runHPA(t *testing.T, snap *cluster.Snapshot) []Finding {
	t.Helper()
	fs, err := (&OrphanedHPA{}).Run(context.Background(), snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestOrphanedHPAMissingDeployment(t *testing.T) {
	snap := &cluster.Snapshot{
		HPAs:        []autoscalingv2.HorizontalPodAutoscaler{testHPA("default", "web", "apps/v1", "Deployment", "web-old")},
		Deployments: []metav1.PartialObjectMetadata{workloadMeta("default", "web")},
	}
	fs := runHPA(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityWarning || fs[0].Kind != "HorizontalPodAutoscaler" {
		t.Fatalf("expected 1 WARNING finding, got %+v", fs)
	}
	want := Ref{Kind: "Deployment", Namespace: "default", Name: "web-old"}
	if len(fs[0].Related) != 1 || fs[0].Related[0] != want {
		t.Fatalf("expected related %v, got %v", want, fs[0].Related)
	}
}

func TestOrphanedHPATargetsExist(t *testing.T) {
	snap := &cluster.Snapshot{
		HPAs: []autoscalingv2.HorizontalPodAutoscaler{
			testHPA("default", "web", "apps/v1", "Deployment", "web"),
			testHPA("default", "db", "", "StatefulSet", "db"),
			testHPA("default", "rs", "apps/v1", "ReplicaSet", "rs-1"),
		},
		Deployments:  []metav1.PartialObjectMetadata{workloadMeta("default", "web")},
		StatefulSets: []metav1.PartialObjectMetadata{workloadMeta("default", "db")},
		ReplicaSets:  []metav1.PartialObjectMetadata{workloadMeta("default", "rs-1")},
	}
	if fs := runHPA(t, snap); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %+v", fs)
	}
}

func TestOrphanedHPASkipsUnverifiableTargets(t *testing.T) {
	snap := &cluster.Snapshot{
		HPAs: []autoscalingv2.HorizontalPodAutoscaler{
			testHPA("default", "rollout", "argoproj.io/v1alpha1", "Rollout", "web"),
			testHPA("default", "foreign", "example.com/v1", "Deployment", "web"),
		},
	}
	if fs := runHPA(t, snap); len(fs) != 0 {
		t.Fatalf("custom scale targets cannot be verified and must be skipped, got %+v", fs)
	}
}

func TestOrphanedHPAIgnoreAnnotation(t *testing.T) {
	hpa := testHPA("default", "web", "apps/v1", "Deployment", "gone")
	annotateIgnore(&hpa.ObjectMeta, "orphaned-hpa")
	snap := &cluster.Snapshot{HPAs: []autoscalingv2.HorizontalPodAutoscaler{hpa}}
	if fs := runHPA(t, snap); len(fs) != 0 {
		t.Fatalf("annotated HPA must be skipped, got %+v", fs)
	}
}

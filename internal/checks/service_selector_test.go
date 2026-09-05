package checks

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func runSelector(t *testing.T, snap *cluster.Snapshot) []Finding {
	t.Helper()
	fs, err := (&ServiceSelector{}).Run(context.Background(), snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestServiceSelectorNoMatch(t *testing.T) {
	snap := &cluster.Snapshot{
		Services: []corev1.Service{testService("default", "web", map[string]string{"app": "web"})},
		Pods:     []corev1.Pod{testPod("default", "api-1", map[string]string{"app": "api"})},
	}
	fs := runSelector(t, snap)
	if len(fs) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(fs))
	}
	if fs[0].Severity != SeverityWarning {
		t.Fatalf("expected WARNING, got %s", fs[0].Severity)
	}
	if fs[0].Object() != "Service/web" {
		t.Fatalf("unexpected object %q", fs[0].Object())
	}
}

func TestServiceSelectorMatch(t *testing.T) {
	snap := &cluster.Snapshot{
		Services: []corev1.Service{testService("default", "web", map[string]string{"app": "web"})},
		Pods:     []corev1.Pod{testPod("default", "web-1", map[string]string{"app": "web", "extra": "label"})},
	}
	if fs := runSelector(t, snap); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %d: %+v", len(fs), fs)
	}
}

func TestServiceSelectorPodInOtherNamespaceDoesNotCount(t *testing.T) {
	snap := &cluster.Snapshot{
		Services: []corev1.Service{testService("default", "web", map[string]string{"app": "web"})},
		Pods:     []corev1.Pod{testPod("other", "web-1", map[string]string{"app": "web"})},
	}
	if fs := runSelector(t, snap); len(fs) != 1 {
		t.Fatalf("expected 1 finding, got %+v", fs)
	}
}

func TestServiceSelectorSkipsSelectorlessAndExternalName(t *testing.T) {
	ext := testService("default", "ext", map[string]string{"app": "x"})
	ext.Spec.Type = corev1.ServiceTypeExternalName
	snap := &cluster.Snapshot{
		Services: []corev1.Service{testService("default", "manual-endpoints", nil), ext},
	}
	if fs := runSelector(t, snap); len(fs) != 0 {
		t.Fatalf("selector-less and ExternalName services must be skipped, got %d findings", len(fs))
	}
}

func TestServiceSelectorRespectsNamespaceScope(t *testing.T) {
	snap := &cluster.Snapshot{
		Namespace: "default",
		Services: []corev1.Service{
			testService("other", "api", map[string]string{"app": "api"}), // pods of "other" were not fetched
			testService("default", "web", map[string]string{"app": "web"}),
		},
	}
	fs := runSelector(t, snap)
	if len(fs) != 1 || fs[0].Namespace != "default" {
		t.Fatalf("expected only the in-scope finding, got %+v", fs)
	}
}

func TestServiceSelectorIgnoreAnnotation(t *testing.T) {
	svc := testService("default", "keda-scaled", map[string]string{"app": "batch"})
	annotateIgnore(&svc.ObjectMeta, "service-selector, cert-expiry")
	snap := &cluster.Snapshot{Services: []corev1.Service{svc}}
	if fs := runSelector(t, snap); len(fs) != 0 {
		t.Fatalf("annotated service must be skipped, got %+v", fs)
	}
}

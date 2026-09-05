package checks

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func testRoute(ns, name, to string, alternates ...string) unstructured.Unstructured {
	spec := map[string]interface{}{
		"to": map[string]interface{}{"kind": "Service", "name": to},
	}
	if len(alternates) > 0 {
		var alts []interface{}
		for _, a := range alternates {
			alts = append(alts, map[string]interface{}{"kind": "Service", "name": a, "weight": int64(10)})
		}
		spec["alternateBackends"] = alts
	}
	return unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "route.openshift.io/v1",
		"kind":       "Route",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       spec,
	}}
}

func runRoute(t *testing.T, snap *cluster.Snapshot) []Finding {
	t.Helper()
	fs, err := (&OpenShiftRoute{}).Run(context.Background(), snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestOpenShiftRouteNoopWithoutRouteAPI(t *testing.T) {
	snap := &cluster.Snapshot{HasRoutes: false, Routes: []unstructured.Unstructured{testRoute("default", "web", "gone")}}
	if fs := runRoute(t, snap); len(fs) != 0 {
		t.Fatalf("without the Route API the check is a no-op, got %+v", fs)
	}
}

func TestOpenShiftRouteMissingService(t *testing.T) {
	snap := &cluster.Snapshot{HasRoutes: true, Routes: []unstructured.Unstructured{testRoute("default", "web", "gone")}}
	fs := runRoute(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityCritical || fs[0].Kind != "Route" || fs[0].Name != "web" {
		t.Fatalf("expected 1 CRITICAL route finding, got %+v", fs)
	}
}

func TestOpenShiftRouteAlternateBackends(t *testing.T) {
	snap := &cluster.Snapshot{
		HasRoutes: true,
		Routes:    []unstructured.Unstructured{testRoute("default", "web", "web", "web-canary", "web-v2")},
		Services:  []corev1.Service{testService("default", "web", nil), testService("default", "web-v2", nil)},
	}
	fs := runRoute(t, snap)
	if len(fs) != 1 || fs[0].Key != "spec.alternateBackends:web-canary" {
		t.Fatalf("expected the missing alternate backend only, got %+v", fs)
	}
}

func TestOpenShiftRouteHealthy(t *testing.T) {
	snap := &cluster.Snapshot{
		HasRoutes: true,
		Routes:    []unstructured.Unstructured{testRoute("default", "web", "web")},
		Services:  []corev1.Service{testService("default", "web", nil)},
	}
	if fs := runRoute(t, snap); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %+v", fs)
	}
}

func TestOpenShiftRouteIgnoreAnnotation(t *testing.T) {
	r := testRoute("default", "web", "gone")
	r.SetAnnotations(map[string]string{IgnoreAnnotation: "openshift-route"})
	snap := &cluster.Snapshot{HasRoutes: true, Routes: []unstructured.Unstructured{r}}
	if fs := runRoute(t, snap); len(fs) != 0 {
		t.Fatalf("annotated route must be skipped, got %+v", fs)
	}
}

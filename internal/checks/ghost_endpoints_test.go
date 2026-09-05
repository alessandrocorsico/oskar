package checks

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func runGhost(t *testing.T, snap *cluster.Snapshot) []Finding {
	t.Helper()
	fs, err := (&GhostEndpoints{}).Run(context.Background(), snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestGhostEndpointsStalePod(t *testing.T) {
	snap := &cluster.Snapshot{
		EndpointSlices: []discoveryv1.EndpointSlice{testSlice("default", "web-abc", "web", "10.0.0.7", "web-1")},
	}
	fs := runGhost(t, snap)
	if len(fs) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(fs))
	}
	f := fs[0]
	if f.Severity != SeverityCritical {
		t.Fatalf("expected CRITICAL, got %s", f.Severity)
	}
	if f.Kind != "EndpointSlice" || f.Name != "web-abc" || f.Namespace != "default" {
		t.Fatalf("unexpected object %s/%s/%s", f.Namespace, f.Kind, f.Name)
	}
	want := Ref{Kind: "Service", Namespace: "default", Name: "web"}
	if len(f.Related) != 1 || f.Related[0] != want {
		t.Fatalf("expected related %v, got %v", want, f.Related)
	}
	if f.Key != "10.0.0.7" {
		t.Fatalf("expected the endpoint address as key, got %q", f.Key)
	}
}

func TestGhostEndpointsIPMismatch(t *testing.T) {
	snap := &cluster.Snapshot{
		EndpointSlices: []discoveryv1.EndpointSlice{testSlice("default", "web-abc", "web", "10.0.0.7", "web-1")},
		Pods:           []corev1.Pod{testPodWithIP("default", "web-1", "10.0.0.9")},
	}
	fs := runGhost(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityCritical {
		t.Fatalf("expected 1 CRITICAL finding, got %+v", fs)
	}
}

func TestGhostEndpointsHealthy(t *testing.T) {
	snap := &cluster.Snapshot{
		EndpointSlices: []discoveryv1.EndpointSlice{testSlice("default", "web-abc", "web", "10.0.0.7", "web-1")},
		Pods:           []corev1.Pod{testPodWithIP("default", "web-1", "10.0.0.7")},
	}
	if fs := runGhost(t, snap); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %d: %+v", len(fs), fs)
	}
}

func TestGhostEndpointsDualStack(t *testing.T) {
	es := testSlice("default", "web-v6", "web", "fd00::7", "web-1")
	es.AddressType = discoveryv1.AddressTypeIPv6
	pod := testPodWithIP("default", "web-1", "10.0.0.7")
	pod.Status.PodIPs = append(pod.Status.PodIPs, corev1.PodIP{IP: "fd00::7"})
	snap := &cluster.Snapshot{EndpointSlices: []discoveryv1.EndpointSlice{es}, Pods: []corev1.Pod{pod}}
	if fs := runGhost(t, snap); len(fs) != 0 {
		t.Fatalf("secondary pod IPs must match, got %+v", fs)
	}
}

func TestGhostEndpointsSkipsUnmanagedSlices(t *testing.T) {
	noLabels := testSlice("default", "manual", "", "10.0.0.7", "gone-pod")
	noLabels.Labels = nil
	mirrored := testSlice("default", "mirrored", "web", "10.0.0.7", "gone-pod")
	mirrored.Labels[discoveryv1.LabelManagedBy] = "endpointslicemirroring-controller.k8s.io"
	snap := &cluster.Snapshot{EndpointSlices: []discoveryv1.EndpointSlice{noLabels, mirrored}}
	if fs := runGhost(t, snap); len(fs) != 0 {
		t.Fatalf("slices not written by the endpoint controller must be skipped, got %d findings", len(fs))
	}
}

func TestGhostEndpointsSkipsNotReadyEndpoints(t *testing.T) {
	es := testSlice("default", "web-abc", "web", "10.0.0.7", "gone-pod")
	es.Endpoints[0].Conditions.Ready = ptr(false)
	snap := &cluster.Snapshot{EndpointSlices: []discoveryv1.EndpointSlice{es}}
	if fs := runGhost(t, snap); len(fs) != 0 {
		t.Fatalf("a not-ready endpoint receives no traffic and must not be reported, got %+v", fs)
	}
}

func TestGhostEndpointsRespectsNamespaceScope(t *testing.T) {
	snap := &cluster.Snapshot{
		Namespace: "default",
		EndpointSlices: []discoveryv1.EndpointSlice{
			testSlice("other", "api-abc", "api", "10.0.1.1", "api-1"),   // pods of "other" were not fetched
			testSlice("default", "web-abc", "web", "10.0.0.7", "web-1"), // genuinely stale
		},
	}
	fs := runGhost(t, snap)
	if len(fs) != 1 || fs[0].Namespace != "default" {
		t.Fatalf("expected only the in-scope finding, got %+v", fs)
	}
}

func TestGhostEndpointsIgnoreAnnotation(t *testing.T) {
	svc := testService("default", "web", nil)
	annotateIgnore(&svc.ObjectMeta, "ghost-endpoints")
	snap := &cluster.Snapshot{
		Services:       []corev1.Service{svc},
		EndpointSlices: []discoveryv1.EndpointSlice{testSlice("default", "web-abc", "web", "10.0.0.7", "gone")},
	}
	if fs := runGhost(t, snap); len(fs) != 0 {
		t.Fatalf("annotated Service must silence the check, got %+v", fs)
	}

	es := testSlice("default", "web-abc", "web", "10.0.0.7", "gone")
	annotateIgnore(&es.ObjectMeta, "all")
	snap = &cluster.Snapshot{EndpointSlices: []discoveryv1.EndpointSlice{es}}
	if fs := runGhost(t, snap); len(fs) != 0 {
		t.Fatalf("annotated slice must silence the check, got %+v", fs)
	}
}

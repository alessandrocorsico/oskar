package checks

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func testIngress(ns, name, svc string, port networkingv1.ServiceBackendPort) networkingv1.Ingress {
	pt := networkingv1.PathTypePrefix
	return networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: "app.example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Path:     "/",
				PathType: &pt,
				Backend:  networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc, Port: port}},
			}}}},
		}}},
	}
}

func svcWithPorts(ns, name string, ports ...corev1.ServicePort) corev1.Service {
	svc := testService(ns, name, map[string]string{"app": name})
	svc.Spec.Ports = ports
	return svc
}

func runIngress(t *testing.T, snap *cluster.Snapshot) []Finding {
	t.Helper()
	fs, err := (&BrokenIngress{}).Run(context.Background(), snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestBrokenIngressMissingService(t *testing.T) {
	snap := &cluster.Snapshot{
		Ingresses: []networkingv1.Ingress{testIngress("default", "web", "web", networkingv1.ServiceBackendPort{Number: 80})},
	}
	fs := runIngress(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityCritical {
		t.Fatalf("expected 1 CRITICAL finding, got %+v", fs)
	}
	f := fs[0]
	if f.Kind != "Ingress" || f.Name != "web" || f.Key != `rule "app.example.com" path "/"` {
		t.Fatalf("unexpected finding %+v", f)
	}
	want := Ref{Kind: "Service", Namespace: "default", Name: "web"}
	if len(f.Related) != 1 || f.Related[0] != want {
		t.Fatalf("expected related %v, got %v", want, f.Related)
	}
}

func TestBrokenIngressMissingPort(t *testing.T) {
	snap := &cluster.Snapshot{
		Ingresses: []networkingv1.Ingress{testIngress("default", "web", "web", networkingv1.ServiceBackendPort{Number: 8080})},
		Services:  []corev1.Service{svcWithPorts("default", "web", corev1.ServicePort{Name: "http", Port: 80})},
	}
	fs := runIngress(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityCritical {
		t.Fatalf("expected 1 CRITICAL finding, got %+v", fs)
	}
}

func TestBrokenIngressPortByNameAndNumber(t *testing.T) {
	snap := &cluster.Snapshot{
		Ingresses: []networkingv1.Ingress{
			testIngress("default", "by-name", "web", networkingv1.ServiceBackendPort{Name: "http"}),
			testIngress("default", "by-number", "web", networkingv1.ServiceBackendPort{Number: 80}),
		},
		Services: []corev1.Service{svcWithPorts("default", "web", corev1.ServicePort{Name: "http", Port: 80})},
	}
	if fs := runIngress(t, snap); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %+v", fs)
	}
}

func TestBrokenIngressDefaultBackend(t *testing.T) {
	ing := networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "fallback", Namespace: "default"},
		Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{
			Service: &networkingv1.IngressServiceBackend{Name: "gone", Port: networkingv1.ServiceBackendPort{Number: 80}},
		}},
	}
	snap := &cluster.Snapshot{Ingresses: []networkingv1.Ingress{ing}}
	fs := runIngress(t, snap)
	if len(fs) != 1 || fs[0].Key != "default backend" {
		t.Fatalf("expected the default backend finding, got %+v", fs)
	}
}

func TestBrokenIngressSkipsResourceBackends(t *testing.T) {
	pt := networkingv1.PathTypePrefix
	ing := networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "bucket", Namespace: "default"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Path: "/", PathType: &pt,
				Backend: networkingv1.IngressBackend{Resource: &corev1.TypedLocalObjectReference{Kind: "StorageBucket", Name: "assets"}},
			}}}},
		}}},
	}
	snap := &cluster.Snapshot{Ingresses: []networkingv1.Ingress{ing}}
	if fs := runIngress(t, snap); len(fs) != 0 {
		t.Fatalf("resource backends are out of scope, got %+v", fs)
	}
}

func TestBrokenIngressIgnoreAnnotation(t *testing.T) {
	ing := testIngress("default", "web", "gone", networkingv1.ServiceBackendPort{Number: 80})
	annotateIgnore(&ing.ObjectMeta, "true")
	snap := &cluster.Snapshot{Ingresses: []networkingv1.Ingress{ing}}
	if fs := runIngress(t, snap); len(fs) != 0 {
		t.Fatalf("annotated ingress must be skipped, got %+v", fs)
	}
}

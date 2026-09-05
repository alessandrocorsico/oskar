package checks

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func testValidatingWebhook(name, svcNS, svcName string, fp *admissionv1.FailurePolicyType) admissionv1.ValidatingWebhookConfiguration {
	return admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name:          "validate.example.com",
			FailurePolicy: fp,
			ClientConfig: admissionv1.WebhookClientConfig{
				Service: &admissionv1.ServiceReference{Namespace: svcNS, Name: svcName},
			},
		}},
	}
}

func runWebhook(t *testing.T, snap *cluster.Snapshot) []Finding {
	t.Helper()
	fs, err := (&DeadWebhook{}).Run(context.Background(), snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestDeadWebhookMissingService(t *testing.T) {
	snap := &cluster.Snapshot{
		ValidatingWebhooks: []admissionv1.ValidatingWebhookConfiguration{testValidatingWebhook("policy", "policy", "policy-svc", nil)},
	}
	fs := runWebhook(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityCritical {
		t.Fatalf("expected 1 CRITICAL finding, got %+v", fs)
	}
	f := fs[0]
	if f.Kind != "ValidatingWebhookConfiguration" || f.Name != "policy" || f.Namespace != "" {
		t.Fatalf("unexpected object %+v", f)
	}
	want := Ref{Kind: "Service", Namespace: "policy", Name: "policy-svc"}
	if len(f.Related) != 1 || f.Related[0] != want {
		t.Fatalf("expected related %v, got %v", want, f.Related)
	}
	if f.Key != "validate.example.com" {
		t.Fatalf("expected the webhook name as key, got %q", f.Key)
	}
}

func TestDeadWebhookNoReadyEndpoints(t *testing.T) {
	notReady := testSlice("policy", "policy-svc-x", "policy-svc", "10.0.0.5", "policy-1")
	notReady.Endpoints[0].Conditions.Ready = ptr(false)
	snap := &cluster.Snapshot{
		ValidatingWebhooks: []admissionv1.ValidatingWebhookConfiguration{testValidatingWebhook("policy", "policy", "policy-svc", nil)},
		Services:           []corev1.Service{testService("policy", "policy-svc", map[string]string{"app": "policy"})},
		EndpointSlices:     []discoveryv1.EndpointSlice{notReady},
	}
	fs := runWebhook(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityCritical {
		t.Fatalf("expected 1 CRITICAL finding, got %+v", fs)
	}
}

func TestDeadWebhookHealthy(t *testing.T) {
	snap := &cluster.Snapshot{
		ValidatingWebhooks: []admissionv1.ValidatingWebhookConfiguration{testValidatingWebhook("policy", "policy", "policy-svc", nil)},
		Services:           []corev1.Service{testService("policy", "policy-svc", map[string]string{"app": "policy"})},
		EndpointSlices:     []discoveryv1.EndpointSlice{testSlice("policy", "policy-svc-x", "policy-svc", "10.0.0.5", "policy-1")},
	}
	if fs := runWebhook(t, snap); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %+v", fs)
	}
}

func TestDeadWebhookIgnorePolicyIsWarning(t *testing.T) {
	snap := &cluster.Snapshot{
		ValidatingWebhooks: []admissionv1.ValidatingWebhookConfiguration{testValidatingWebhook("policy", "policy", "policy-svc", ptr(admissionv1.Ignore))},
	}
	fs := runWebhook(t, snap)
	if len(fs) != 1 || fs[0].Severity != SeverityWarning {
		t.Fatalf("expected 1 WARNING finding, got %+v", fs)
	}
}

func TestDeadWebhookSkipsURLBackends(t *testing.T) {
	url := "https://hooks.example.com/validate"
	cfg := admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "external"},
		Webhooks:   []admissionv1.ValidatingWebhook{{Name: "ext", ClientConfig: admissionv1.WebhookClientConfig{URL: &url}}},
	}
	snap := &cluster.Snapshot{ValidatingWebhooks: []admissionv1.ValidatingWebhookConfiguration{cfg}}
	if fs := runWebhook(t, snap); len(fs) != 0 {
		t.Fatalf("URL webhooks cannot be verified and must be skipped, got %+v", fs)
	}
}

func TestDeadWebhookMutating(t *testing.T) {
	cfg := admissionv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "inject"},
		Webhooks: []admissionv1.MutatingWebhook{{
			Name:         "inject.example.com",
			ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: "mesh", Name: "injector"}},
		}},
	}
	snap := &cluster.Snapshot{MutatingWebhooks: []admissionv1.MutatingWebhookConfiguration{cfg}}
	fs := runWebhook(t, snap)
	if len(fs) != 1 || fs[0].Kind != "MutatingWebhookConfiguration" {
		t.Fatalf("expected 1 mutating finding, got %+v", fs)
	}
}

func TestDeadWebhookIgnoreAnnotation(t *testing.T) {
	cfg := testValidatingWebhook("policy", "policy", "policy-svc", nil)
	annotateIgnore(&cfg.ObjectMeta, "dead-webhook")
	snap := &cluster.Snapshot{ValidatingWebhooks: []admissionv1.ValidatingWebhookConfiguration{cfg}}
	if fs := runWebhook(t, snap); len(fs) != 0 {
		t.Fatalf("annotated configuration must be skipped, got %+v", fs)
	}
}

func TestDeadWebhookNeedsClusterWideServices(t *testing.T) {
	for _, n := range (&DeadWebhook{}).Needs() {
		if (n.Resource == cluster.ResourceServices || n.Resource == cluster.ResourceEndpointSlices) && !n.ClusterWide {
			t.Fatalf("%s must be requested cluster-wide: webhooks reference services in any namespace", n.Resource)
		}
	}
}

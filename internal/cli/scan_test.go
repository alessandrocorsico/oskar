package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/alessandrocorsico/oskar/internal/checks"
	"github.com/alessandrocorsico/oskar/internal/cluster"
	"github.com/alessandrocorsico/oskar/internal/report"
)

func fakeClients(t *testing.T, objs ...runtime.Object) (*kubefake.Clientset, cluster.Clients) {
	t.Helper()
	kube := kubefake.NewClientset(objs...)
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return kube, cluster.Clients{
		Kube:     kube,
		Dynamic:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		Metadata: metadatafake.NewSimpleMetadataClient(scheme),
	}
}

func testOptions(out io.Writer, clients cluster.Clients) *scanOptions {
	return &scanOptions{
		output:         "json",
		failOn:         "warning",
		certWarnDays:   30,
		stuckThreshold: time.Hour,
		out:            out,
		errOut:         io.Discard,
		connect: func(*scanOptions) (*cluster.Connection, cluster.Clients, error) {
			return &cluster.Connection{Context: "test-ctx", Server: "https://fake.invalid:6443"}, clients, nil
		},
	}
}

func webhookTo(ns, name string) *admissionv1.ValidatingWebhookConfiguration {
	return &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "policy"},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name:         "validate.example.com",
			ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: ns, Name: name}},
		}},
	}
}

func readySlice(ns, svc string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: svc + "-abc", Namespace: ns, Labels: map[string]string{
			discoveryv1.LabelServiceName: svc,
			discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
		}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}}},
	}
}

func decode(t *testing.T, buf *bytes.Buffer) report.Output {
	t.Helper()
	var out report.Output
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	return out
}

func TestFailOnRank(t *testing.T) {
	for in, want := range map[string]int{"never": 0, "info": 1, "warning": 2, "critical": 3, "CRITICAL": 3} {
		got, err := failOnRank(in)
		if err != nil || got != want {
			t.Errorf("failOnRank(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := failOnRank("fatal"); err == nil {
		t.Error("invalid --fail-on must be rejected")
	}
}

func TestExitCode(t *testing.T) {
	if ExitCode(nil) != ExitClean || ExitCode(ErrFindings) != ExitFindings || ExitCode(errors.New("x")) != ExitError {
		t.Fatal("exit code mapping is wrong")
	}
	if ExitCode(errors.Join(errors.New("wrapped"), ErrFindings)) != ExitFindings {
		t.Fatal("wrapped ErrFindings must still map to exit 2")
	}
}

func TestDropNamespaces(t *testing.T) {
	fs := []checks.Finding{
		{Namespace: "kube-system", Name: "a"},
		{Namespace: "app", Name: "b"},
		{Name: "cluster-scoped"},
	}
	got := dropNamespaces(fs, []string{"kube-system", " app "})
	if len(got) != 1 || got[0].Name != "cluster-scoped" {
		t.Fatalf("unexpected result %+v", got)
	}
	if len(dropNamespaces(fs, nil)) != 3 {
		t.Fatal("no exclusions must keep everything")
	}
}

func TestRunScanJSONEndToEnd(t *testing.T) {
	kube, clients := fakeClients(t, webhookTo("policy", "policy-svc"))
	kube.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied"))
	})
	var buf bytes.Buffer
	o := testOptions(&buf, clients)
	o.failOn = "critical"

	err := runScan(context.Background(), o)
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("a CRITICAL finding must yield ErrFindings, got %v", err)
	}
	out := decode(t, &buf)

	if out.SchemaVersion != report.SchemaVersion || out.Scan.Context != "test-ctx" || out.Scan.Server != "https://fake.invalid:6443" || out.Scan.OskarVersion != version {
		t.Fatalf("scan metadata missing: %+v", out.Scan)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("expected exactly the dead-webhook finding, got %+v", out.Findings)
	}
	f := out.Findings[0]
	if f.Check != "dead-webhook" || f.Kind != "ValidatingWebhookConfiguration" || f.Name != "policy" || f.Fingerprint == "" {
		t.Fatalf("unexpected finding %+v", f)
	}

	states := map[string]checks.Status{}
	for _, st := range out.Scan.Checks {
		states[st.Name] = st
	}
	for _, name := range []string{"ghost-endpoints", "service-selector", "stuck-terminating"} {
		if states[name].State != checks.StateSkipped {
			t.Fatalf("%s needs pods and must be skipped, got %+v", name, states[name])
		}
	}
	if states["dead-webhook"].State != checks.StateOK || states["dead-webhook"].Findings != 1 {
		t.Fatalf("unexpected dead-webhook status %+v", states["dead-webhook"])
	}
	if out.Summary.ChecksSkipped < 3 || out.Summary.Critical != 1 || out.Summary.Total != 1 {
		t.Fatalf("unexpected summary %+v", out.Summary)
	}
	joined := strings.Join(out.Notices, "\n")
	if !strings.Contains(joined, "pods") || !strings.Contains(joined, "ghost-endpoints skipped") {
		t.Fatalf("notices should explain the RBAC denial and the skips, got %v", out.Notices)
	}
}

func TestRunScanNamespaceDoesNotBreakWebhooks(t *testing.T) {
	_, clients := fakeClients(t,
		webhookTo("cert-manager", "cert-manager-webhook"),
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager-webhook", Namespace: "cert-manager"}},
		readySlice("cert-manager", "cert-manager-webhook"),
	)
	var buf bytes.Buffer
	o := testOptions(&buf, clients)
	o.namespace = "default"

	if err := runScan(context.Background(), o); err != nil {
		t.Fatalf("expected a clean scan, got %v", err)
	}
	out := decode(t, &buf)
	if len(out.Findings) != 0 {
		t.Fatalf("a webhook backed by a Service in another namespace is healthy, got %+v", out.Findings)
	}
	if out.Scan.Namespace != "default" {
		t.Fatalf("scan namespace should be recorded, got %+v", out.Scan)
	}
}

func TestRunScanNetworkErrorAborts(t *testing.T) {
	kube, clients := fakeClients(t)
	kube.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("dial tcp: connection refused")
	})
	var buf bytes.Buffer
	err := runScan(context.Background(), testOptions(&buf, clients))
	if err == nil || errors.Is(err, ErrFindings) || !strings.Contains(err.Error(), "building cluster snapshot") {
		t.Fatalf("an unreachable API server must be a runtime error, got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("no report must be written on a failed scan, got %q", buf.String())
	}
}

func TestRunScanCleanTable(t *testing.T) {
	_, clients := fakeClients(t)
	var buf bytes.Buffer
	o := testOptions(&buf, clients)
	o.output = "table"
	if err := runScan(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No anomalies found") {
		t.Fatalf("unexpected table output:\n%s", buf.String())
	}
}

func TestRunScanExcludeNamespacesAndFailOn(t *testing.T) {
	_, clients := fakeClients(t,
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "idle", Namespace: "sandbox"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "none"}}},
	)
	var buf bytes.Buffer
	o := testOptions(&buf, clients)
	if err := runScan(context.Background(), o); !errors.Is(err, ErrFindings) {
		t.Fatalf("service-selector WARNING with --fail-on warning must yield ErrFindings, got %v", err)
	}

	buf.Reset()
	o.excludeNamespaces = []string{"sandbox"}
	if err := runScan(context.Background(), o); err != nil {
		t.Fatalf("excluded namespace should hide the finding, got %v", err)
	}
	if out := decode(t, &buf); len(out.Findings) != 0 {
		t.Fatalf("expected no findings, got %+v", out.Findings)
	}

	buf.Reset()
	o.excludeNamespaces = nil
	o.failOn = "critical"
	if err := runScan(context.Background(), o); err != nil {
		t.Fatalf("a WARNING is below --fail-on critical, got %v", err)
	}
}

func TestRunScanRejectsBadFlags(t *testing.T) {
	_, clients := fakeClients(t)
	o := testOptions(io.Discard, clients)
	o.output = "yaml"
	if err := runScan(context.Background(), o); err == nil {
		t.Fatal("unknown output must be rejected")
	}
	o = testOptions(io.Discard, clients)
	o.only = []string{"nope"}
	if err := runScan(context.Background(), o); err == nil {
		t.Fatal("unknown check must be rejected")
	}
	o = testOptions(io.Discard, clients)
	o.only = []string{"cert-expiry"}
	o.skip = []string{"cert-expiry"}
	if err := runScan(context.Background(), o); err == nil || !strings.Contains(err.Error(), "no checks selected") {
		t.Fatalf("empty selection must be rejected, got %v", err)
	}
}

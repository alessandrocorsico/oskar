package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
)

type fakes struct {
	kube    *kubefake.Clientset
	clients Clients
}

// newFakes builds the three fake clients from a mixed bag of objects: typed
// objects go to the clientset, PartialObjectMetadata to the metadata client,
// unstructured objects (Routes) to the dynamic client.
func newFakes(t *testing.T, objs ...runtime.Object) *fakes {
	t.Helper()
	var typed, meta, routes []runtime.Object
	for _, o := range objs {
		switch o.(type) {
		case *metav1.PartialObjectMetadata:
			meta = append(meta, o)
		case *unstructured.Unstructured:
			routes = append(routes, o)
		default:
			typed = append(typed, o)
		}
	}
	kube := kubefake.NewClientset(typed...)
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	md := metadatafake.NewSimpleMetadataClient(scheme, meta...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{routeGVR: "RouteList"}, routes...)
	return &fakes{kube: kube, clients: Clients{Kube: kube, Dynamic: dyn, Metadata: md}}
}

func forbidden(resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("RBAC denied"))
}

// denyList makes listing resource fail with 403; with clusterWideOnly only
// the all-namespaces listing is denied.
func (f *fakes) denyList(resource string, clusterWideOnly bool) {
	f.kube.PrependReactor("list", resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		if clusterWideOnly && a.GetNamespace() != "" {
			return false, nil, nil
		}
		return true, nil, forbidden(resource)
	})
}

func (f *fakes) failList(resource string, err error) {
	f.kube.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
}

func (f *fakes) listed(resource string) []string {
	var scopes []string
	for _, a := range f.kube.Actions() {
		if a.GetVerb() == "list" && a.GetResource().Resource == resource {
			scopes = append(scopes, a.GetNamespace())
		}
	}
	return scopes
}

func pod(ns, name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func service(ns, name string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func deployment(ns, name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
}

func route(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": routeGroupVersion,
		"kind":       "Route",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       map[string]interface{}{"to": map[string]interface{}{"kind": "Service", "name": "web"}},
	}}
}

func TestBuildSnapshotFetchesOnlyNeeds(t *testing.T) {
	f := newFakes(t, pod("default", "p1"), service("default", "s1"))
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourceServices)})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Services) != 1 || !s.Available(ResourceServices) {
		t.Fatalf("services should be listed, got %+v", s.Services)
	}
	if len(s.Pods) != 0 || s.Available(ResourcePods) {
		t.Fatal("pods were not needed and must not be fetched")
	}
	if got := f.listed("pods"); len(got) != 0 {
		t.Fatalf("no pods list call expected, got %v", got)
	}
	if reason := s.Unmet(Needs(ResourcePods)); !strings.Contains(reason, "not fetched") {
		t.Fatalf("unexpected reason %q", reason)
	}
}

func TestBuildSnapshotForbiddenDegrades(t *testing.T) {
	f := newFakes(t, pod("default", "p1"), service("default", "s1"))
	f.denyList("pods", false)
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourcePods, ResourceServices)})
	if err != nil {
		t.Fatalf("a 403 must degrade, not abort: %v", err)
	}
	if s.Available(ResourcePods) || !s.Available(ResourceServices) {
		t.Fatalf("pods unavailable, services available expected; state=%+v", s.state)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "pods") || !strings.Contains(s.Warnings[0], "RBAC") {
		t.Fatalf("expected one RBAC notice about pods, got %v", s.Warnings)
	}
	if reason := s.Unmet(Needs(ResourcePods)); !strings.Contains(reason, "could not be listed") {
		t.Fatalf("unexpected reason %q", reason)
	}
	if reason := s.Unmet([]Need{{Resource: ResourcePods, Optional: true}}); reason != "" {
		t.Fatalf("optional needs never block a check, got %q", reason)
	}
}

func TestBuildSnapshotNotServedDegrades(t *testing.T) {
	f := newFakes(t)
	f.failList("horizontalpodautoscalers", apierrors.NewNotFound(schema.GroupResource{Group: "autoscaling", Resource: "horizontalpodautoscalers"}, ""))
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourceHPAs)})
	if err != nil {
		t.Fatalf("a 404 (API not served) must degrade, not abort: %v", err)
	}
	if s.Available(ResourceHPAs) || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "not served") {
		t.Fatalf("expected an 'API not served' notice, got available=%v warnings=%v", s.Available(ResourceHPAs), s.Warnings)
	}
}

func TestBuildSnapshotNetworkErrorIsFatal(t *testing.T) {
	f := newFakes(t)
	f.failList("services", errors.New("dial tcp 10.0.0.1:6443: connect: connection refused"))
	_, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourceServices, ResourcePods)})
	if err == nil || !strings.Contains(err.Error(), "listing services") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("a non-RBAC failure must abort the scan, got %v", err)
	}
}

func TestBuildSnapshotStrictMakesForbiddenFatal(t *testing.T) {
	f := newFakes(t)
	f.denyList("pods", false)
	_, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourcePods), Strict: true})
	if err == nil || !strings.Contains(err.Error(), "listing pods") {
		t.Fatalf("--strict must turn a 403 into an error, got %v", err)
	}
}

func TestBuildSnapshotClusterWideNeedWithNamespace(t *testing.T) {
	f := newFakes(t, service("a", "s1"), service("b", "s2"), pod("a", "p1"), pod("b", "p2"))
	s, err := BuildSnapshot(context.Background(), f.clients, Request{
		Namespace: "a",
		Needs:     []Need{{Resource: ResourceServices, ClusterWide: true}, {Resource: ResourcePods}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Services) != 2 {
		t.Fatalf("services should be listed cluster-wide, got %d", len(s.Services))
	}
	if len(s.Pods) != 1 || s.Pods[0].Namespace != "a" {
		t.Fatalf("pods should be listed in namespace a only, got %+v", s.Pods)
	}
	if reason := s.Unmet([]Need{{Resource: ResourceServices, ClusterWide: true}}); reason != "" {
		t.Fatalf("cluster-wide need should be satisfied, got %q", reason)
	}
	if got := f.listed("services"); len(got) != 1 || got[0] != "" {
		t.Fatalf("expected one cluster-wide services list, got %v", got)
	}
}

func TestBuildSnapshotClusterWideForbiddenFallsBackToNamespace(t *testing.T) {
	f := newFakes(t, service("a", "s1"), service("b", "s2"))
	f.denyList("services", true)
	s, err := BuildSnapshot(context.Background(), f.clients, Request{
		Namespace: "a",
		Needs:     []Need{{Resource: ResourceServices, ClusterWide: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Services) != 1 || s.Services[0].Namespace != "a" {
		t.Fatalf("expected the namespaced fallback listing, got %+v", s.Services)
	}
	if reason := s.Unmet([]Need{{Resource: ResourceServices, ClusterWide: true}}); !strings.Contains(reason, "whole cluster") {
		t.Fatalf("cluster-wide need must be reported unmet, got %q", reason)
	}
	if reason := s.Unmet(Needs(ResourceServices)); reason != "" {
		t.Fatalf("namespaced need must be satisfied, got %q", reason)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "cluster-wide") {
		t.Fatalf("expected a fallback notice, got %v", s.Warnings)
	}
	if got := f.listed("services"); len(got) != 2 || got[0] != "" || got[1] != "a" {
		t.Fatalf("expected a cluster-wide attempt then a namespaced one, got %v", got)
	}
}

func TestBuildSnapshotStrictDoesNotFallBack(t *testing.T) {
	f := newFakes(t, service("a", "s1"))
	f.denyList("services", true)
	_, err := BuildSnapshot(context.Background(), f.clients, Request{
		Namespace: "a",
		Needs:     []Need{{Resource: ResourceServices, ClusterWide: true}},
		Strict:    true,
	})
	if err == nil {
		t.Fatal("--strict must not silently fall back to a narrower listing")
	}
}

func TestBuildSnapshotRoutesAbsent(t *testing.T) {
	f := newFakes(t, route("default", "r1"))
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourceRoutes)})
	if err != nil {
		t.Fatal(err)
	}
	if s.HasRoutes || len(s.Routes) != 0 || !s.Available(ResourceRoutes) || len(s.Warnings) != 0 {
		t.Fatalf("off OpenShift routes must be a silent no-op, got HasRoutes=%v routes=%d warnings=%v", s.HasRoutes, len(s.Routes), s.Warnings)
	}
}

func TestBuildSnapshotRoutesPresent(t *testing.T) {
	f := newFakes(t, route("default", "r1"))
	f.kube.Resources = []*metav1.APIResourceList{{
		GroupVersion: routeGroupVersion,
		APIResources: []metav1.APIResource{{Name: "routes", Kind: "Route", Namespaced: true}},
	}}
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourceRoutes)})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasRoutes || len(s.Routes) != 1 || s.Routes[0].GetName() != "r1" {
		t.Fatalf("expected the route to be listed, got HasRoutes=%v routes=%+v", s.HasRoutes, s.Routes)
	}
}

func TestBuildSnapshotScrubsPrivateKey(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "default"},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte("CERT"),
			corev1.TLSPrivateKeyKey: []byte("PRIVATE"),
		},
	}
	f := newFakes(t, sec)
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourceTLSSecrets)})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.TLSSecrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(s.TLSSecrets))
	}
	if _, ok := s.TLSSecrets[0].Data[corev1.TLSPrivateKeyKey]; ok {
		t.Fatal("tls.key must be dropped from the snapshot")
	}
	if string(s.TLSSecrets[0].Data[corev1.TLSCertKey]) != "CERT" {
		t.Fatal("tls.crt must be kept")
	}
}

func TestBuildSnapshotWorkloadsViaMetadata(t *testing.T) {
	f := newFakes(t, deployment("default", "web"))
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourceDeployments, ResourceStatefulSets)})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasWorkload("Deployment", "default", "web") {
		t.Fatal("deployment should be found through the metadata client")
	}
	if s.HasWorkload("Deployment", "default", "gone") || s.HasWorkload("StatefulSet", "default", "web") || s.HasWorkload("CronJob", "default", "web") {
		t.Fatal("unexpected workload match")
	}
}

func TestBuildSnapshotListsPodsAfterEndpointSlices(t *testing.T) {
	f := newFakes(t)
	s, err := BuildSnapshot(context.Background(), f.clients, Request{Needs: Needs(ResourcePods, ResourceEndpointSlices, ResourceServices)})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Available(ResourcePods) {
		t.Fatal("pods should be available")
	}
	acts := f.kube.Actions()
	if last := acts[len(acts)-1]; last.GetVerb() != "list" || last.GetResource().Resource != "pods" {
		t.Fatalf("pods must be the last resource listed, got %s %s", last.GetVerb(), last.GetResource().Resource)
	}
}

func TestSnapshotHandBuiltIsComplete(t *testing.T) {
	ready := true
	notReady := false
	s := &Snapshot{
		Pods:     []corev1.Pod{*pod("a", "p1"), *pod("b", "p2"), *pod("a", "p3")},
		Services: []corev1.Service{*service("a", "web"), *service("a", "idle"), *service("a", "notready")},
		EndpointSlices: []discoveryv1.EndpointSlice{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "web-1", Labels: map[string]string{discoveryv1.LabelServiceName: "web"}},
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "notready-1", Labels: map[string]string{discoveryv1.LabelServiceName: "notready"}},
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}}}},
		},
	}
	if !s.Available(ResourcePods) || s.Unmet(Needs(ResourcePods, ResourceRoutes)) != "" {
		t.Fatal("a hand-built snapshot must count as complete")
	}
	if got := len(s.PodsInNamespace("a")); got != 2 {
		t.Fatalf("expected 2 pods in a, got %d", got)
	}
	if s.GetPod("b", "p2") == nil || s.GetPod("b", "p1") != nil {
		t.Fatal("pod index is wrong")
	}
	if s.GetService("a", "web") == nil || s.GetService("b", "web") != nil {
		t.Fatal("service index is wrong")
	}
	if !s.ServiceHasReadyEndpoints("a", "web") {
		t.Fatal("web has a ready endpoint")
	}
	if s.ServiceHasReadyEndpoints("a", "idle") || s.ServiceHasReadyEndpoints("a", "notready") {
		t.Fatal("idle and notready must not count as backed")
	}
}

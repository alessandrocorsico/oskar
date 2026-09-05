package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const routeGroupVersion = "route.openshift.io/v1"

var (
	routeGVR       = schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}
	deploymentGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	statefulSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	replicaSetGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
)

// Snapshot is a read-only, point-in-time view of the cluster resources that
// checks operate on. Everything is fetched up front, so checks never issue
// API calls of their own.
//
// A Snapshot built by hand (as unit tests do) is considered complete:
// Available reports true and Unmet reports nothing for every resource.
type Snapshot struct {
	Namespace string // "" means all namespaces

	Pods                   []corev1.Pod
	Services               []corev1.Service
	EndpointSlices         []discoveryv1.EndpointSlice
	Ingresses              []networkingv1.Ingress
	ValidatingWebhooks     []admissionv1.ValidatingWebhookConfiguration
	MutatingWebhooks       []admissionv1.MutatingWebhookConfiguration
	TLSSecrets             []corev1.Secret // kubernetes.io/tls only, tls.key already removed
	PersistentVolumes      []corev1.PersistentVolume
	PersistentVolumeClaims []corev1.PersistentVolumeClaim
	Namespaces             []corev1.Namespace
	HPAs                   []autoscalingv2.HorizontalPodAutoscaler

	// Workloads are fetched as metadata only: checks need to know whether
	// they exist, and a full ReplicaSet list on a large cluster is the single
	// heaviest response the API server could send us.
	Deployments  []metav1.PartialObjectMetadata
	StatefulSets []metav1.PartialObjectMetadata
	ReplicaSets  []metav1.PartialObjectMetadata

	// OpenShift Routes, fetched only when the route.openshift.io API group
	// is served. On any other distribution HasRoutes stays false.
	Routes    []unstructured.Unstructured
	HasRoutes bool

	// Warnings collects resources that could not be listed (RBAC, API group
	// not served) so the scan degrades into notices instead of aborting.
	Warnings []string

	state map[Resource]fetchState

	indexOnce sync.Once
	svcIndex  map[string]*corev1.Service
	podIndex  map[string]*corev1.Pod
	podsByNS  map[string][]*corev1.Pod
	deploySet map[string]struct{}
	stsSet    map[string]struct{}
	rsSet     map[string]struct{}
	readyEP   map[string]struct{}
}

type fetchState struct {
	err         error // nil when the list succeeded
	clusterWide bool  // true when listed across all namespaces
}

// Request describes what BuildSnapshot should fetch.
type Request struct {
	Namespace string // "" means all namespaces
	Needs     []Need // union of what the selected checks read
	Strict    bool   // treat any resource that cannot be listed as fatal
}

// BuildSnapshot lists every resource the request needs.
//
// Errors are classified: a 403 (RBAC) or a 404 (API group not served) mark
// the resource unavailable and add a notice, so checks that depend on it
// are skipped rather than run on empty data; anything else (network,
// timeout, authentication) aborts the scan, because a report produced
// without data would be a false "all clear". With Strict every failure
// aborts.
//
// Lists run concurrently, except Pods, which are fetched last on purpose:
// EndpointSlices are read before the pods they point at, so a pod that
// disappears in between must have completed a full graceful deletion within
// the window, which is far rarer than a pod becoming ready in it.
func BuildSnapshot(ctx context.Context, c Clients, req Request) (*Snapshot, error) {
	if c.Kube == nil {
		return nil, errors.New("no kubernetes client configured")
	}
	s := &Snapshot{Namespace: req.Namespace, state: make(map[Resource]fetchState)}
	f := &fetcher{s: s, ns: req.Namespace, strict: req.Strict, wants: make(map[Resource]bool)}
	for _, n := range req.Needs {
		f.wants[n.Resource] = f.wants[n.Resource] || n.ClusterWide
	}

	opts := metav1.ListOptions{}
	core := c.Kube.CoreV1()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)
	g.Go(fetch(gctx, f, ResourceServices, &s.Services, func(ctx context.Context, ns string) ([]corev1.Service, error) {
		l, err := core.Services(ns).List(ctx, opts)
		return items(l, err, func() []corev1.Service { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceEndpointSlices, &s.EndpointSlices, func(ctx context.Context, ns string) ([]discoveryv1.EndpointSlice, error) {
		l, err := c.Kube.DiscoveryV1().EndpointSlices(ns).List(ctx, opts)
		return items(l, err, func() []discoveryv1.EndpointSlice { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceIngresses, &s.Ingresses, func(ctx context.Context, ns string) ([]networkingv1.Ingress, error) {
		l, err := c.Kube.NetworkingV1().Ingresses(ns).List(ctx, opts)
		return items(l, err, func() []networkingv1.Ingress { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceValidatingWebhooks, &s.ValidatingWebhooks, func(ctx context.Context, _ string) ([]admissionv1.ValidatingWebhookConfiguration, error) {
		l, err := c.Kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, opts)
		return items(l, err, func() []admissionv1.ValidatingWebhookConfiguration { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceMutatingWebhooks, &s.MutatingWebhooks, func(ctx context.Context, _ string) ([]admissionv1.MutatingWebhookConfiguration, error) {
		l, err := c.Kube.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, opts)
		return items(l, err, func() []admissionv1.MutatingWebhookConfiguration { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceTLSSecrets, &s.TLSSecrets, func(ctx context.Context, ns string) ([]corev1.Secret, error) {
		// Only TLS secrets are requested (server-side field selector), and the
		// private key is dropped as soon as the response is decoded: the
		// cert-expiry check needs the certificate alone.
		l, err := core.Secrets(ns).List(ctx, metav1.ListOptions{FieldSelector: "type=" + string(corev1.SecretTypeTLS)})
		if err != nil {
			return nil, err
		}
		for i := range l.Items {
			scrubPrivateKey(&l.Items[i])
		}
		return l.Items, nil
	}))
	g.Go(fetch(gctx, f, ResourcePersistentVolumes, &s.PersistentVolumes, func(ctx context.Context, _ string) ([]corev1.PersistentVolume, error) {
		l, err := core.PersistentVolumes().List(ctx, opts)
		return items(l, err, func() []corev1.PersistentVolume { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourcePersistentVolumeClaims, &s.PersistentVolumeClaims, func(ctx context.Context, ns string) ([]corev1.PersistentVolumeClaim, error) {
		l, err := core.PersistentVolumeClaims(ns).List(ctx, opts)
		return items(l, err, func() []corev1.PersistentVolumeClaim { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceNamespaces, &s.Namespaces, func(ctx context.Context, _ string) ([]corev1.Namespace, error) {
		l, err := core.Namespaces().List(ctx, opts)
		return items(l, err, func() []corev1.Namespace { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceHPAs, &s.HPAs, func(ctx context.Context, ns string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
		l, err := c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, opts)
		return items(l, err, func() []autoscalingv2.HorizontalPodAutoscaler { return l.Items })
	}))
	g.Go(fetch(gctx, f, ResourceDeployments, &s.Deployments, metadataLister(c, deploymentGVR)))
	g.Go(fetch(gctx, f, ResourceStatefulSets, &s.StatefulSets, metadataLister(c, statefulSetGVR)))
	g.Go(fetch(gctx, f, ResourceReplicaSets, &s.ReplicaSets, metadataLister(c, replicaSetGVR)))
	g.Go(func() error { return fetchRoutes(gctx, c, f) })
	if err := g.Wait(); err != nil {
		return nil, err
	}

	if err := fetch(ctx, f, ResourcePods, &s.Pods, func(ctx context.Context, ns string) ([]corev1.Pod, error) {
		l, err := core.Pods(ns).List(ctx, opts)
		return items(l, err, func() []corev1.Pod { return l.Items })
	})(); err != nil {
		return nil, err
	}
	return s, nil
}

// items is a small adapter that turns the (list, err) pair returned by
// client-go into the (items, err) pair fetch expects.
func items[T any, L any](_ L, err error, get func() []T) ([]T, error) {
	if err != nil {
		return nil, err
	}
	return get(), nil
}

func metadataLister(c Clients, gvr schema.GroupVersionResource) lister[metav1.PartialObjectMetadata] {
	return func(ctx context.Context, ns string) ([]metav1.PartialObjectMetadata, error) {
		if c.Metadata == nil {
			return nil, errors.New("no metadata client configured")
		}
		l, err := c.Metadata.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		return l.Items, nil
	}
}

func fetchRoutes(ctx context.Context, c Clients, f *fetcher) error {
	if _, wanted := f.wants[ResourceRoutes]; !wanted {
		return nil
	}
	if _, err := c.Kube.Discovery().ServerResourcesForGroupVersion(routeGroupVersion); err != nil {
		if apierrors.IsNotFound(err) {
			// Not OpenShift: nothing to list, the route check is a no-op.
			f.mu.Lock()
			f.s.state[ResourceRoutes] = fetchState{clusterWide: true}
			f.mu.Unlock()
			return nil
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.fail(ResourceRoutes, err)
	}
	f.s.HasRoutes = true
	return fetch(ctx, f, ResourceRoutes, &f.s.Routes, func(ctx context.Context, ns string) ([]unstructured.Unstructured, error) {
		if c.Dynamic == nil {
			return nil, errors.New("no dynamic client configured")
		}
		l, err := c.Dynamic.Resource(routeGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		return l.Items, nil
	})()
}

func scrubPrivateKey(sec *corev1.Secret) {
	if k, ok := sec.Data[corev1.TLSPrivateKeyKey]; ok {
		clear(k)
		delete(sec.Data, corev1.TLSPrivateKeyKey)
	}
}

// fetcher carries the state shared by the concurrent list tasks.
type fetcher struct {
	s      *Snapshot
	ns     string
	strict bool
	wants  map[Resource]bool // resource -> cluster-wide listing required
	mu     sync.Mutex        // guards s.state and s.Warnings during the build
}

type lister[T any] func(ctx context.Context, ns string) ([]T, error)

// fetch returns a task that lists r into dst and records the outcome.
//
// When a cluster-wide listing is denied while the scan is restricted to a
// namespace, the task falls back to that namespace: namespaced checks keep
// working, and the checks that need the wide view report themselves skipped.
func fetch[T any](ctx context.Context, f *fetcher, r Resource, dst *[]T, list lister[T]) func() error {
	return func() error {
		wide, wanted := f.wants[r]
		if !wanted {
			return nil
		}
		scope := f.ns
		if wide || !namespaced[r] {
			scope = ""
		}
		out, err := list(ctx, scope)
		fellBack := false
		if err != nil && scope == "" && f.ns != "" && namespaced[r] && !f.strict && apierrors.IsForbidden(err) {
			out, err = list(ctx, f.ns)
			scope = f.ns
			fellBack = err == nil
		}

		f.mu.Lock()
		defer f.mu.Unlock()
		if err != nil {
			return f.fail(r, err)
		}
		if fellBack {
			f.s.Warnings = append(f.s.Warnings, fmt.Sprintf("%s: cluster-wide list forbidden by RBAC, listed only namespace %q; checks that need the whole cluster were skipped", r, f.ns))
		}
		*dst = out
		f.s.state[r] = fetchState{clusterWide: scope == ""}
		return nil
	}
}

// fail records a list failure. Callers hold f.mu.
func (f *fetcher) fail(r Resource, err error) error {
	var reason string
	switch {
	case apierrors.IsForbidden(err):
		reason = "forbidden by RBAC"
	case apierrors.IsNotFound(err):
		reason = "API not served by this cluster"
	}
	if reason == "" || f.strict {
		return fmt.Errorf("listing %s: %w", r, err)
	}
	f.s.state[r] = fetchState{err: err}
	f.s.Warnings = append(f.s.Warnings, fmt.Sprintf("could not list %s (%s); checks that need them were skipped", r, reason))
	return nil
}

// Available reports whether resource r was listed successfully. A snapshot
// built by hand (no fetch state) counts as complete.
func (s *Snapshot) Available(r Resource) bool {
	if s.state == nil {
		return true
	}
	st, ok := s.state[r]
	return ok && st.err == nil
}

// Unmet explains why the snapshot cannot serve the given needs, or returns
// "" when every required need is satisfied. Optional needs never count.
func (s *Snapshot) Unmet(needs []Need) string {
	if s.state == nil {
		return ""
	}
	for _, n := range needs {
		if n.Optional {
			continue
		}
		st, ok := s.state[n.Resource]
		switch {
		case !ok:
			return fmt.Sprintf("%s were not fetched", n.Resource)
		case st.err != nil:
			return fmt.Sprintf("%s could not be listed", n.Resource)
		case n.ClusterWide && !st.clusterWide:
			return fmt.Sprintf("%s could only be listed in namespace %q but the check needs the whole cluster", n.Resource, s.Namespace)
		}
	}
	return ""
}

func key(ns, name string) string { return ns + "/" + name }

func (s *Snapshot) index() { s.indexOnce.Do(s.buildIndexes) }

func (s *Snapshot) buildIndexes() {
	s.svcIndex = make(map[string]*corev1.Service, len(s.Services))
	for i := range s.Services {
		s.svcIndex[key(s.Services[i].Namespace, s.Services[i].Name)] = &s.Services[i]
	}
	s.podIndex = make(map[string]*corev1.Pod, len(s.Pods))
	s.podsByNS = make(map[string][]*corev1.Pod)
	for i := range s.Pods {
		p := &s.Pods[i]
		s.podIndex[key(p.Namespace, p.Name)] = p
		s.podsByNS[p.Namespace] = append(s.podsByNS[p.Namespace], p)
	}
	s.deploySet = metaSet(s.Deployments)
	s.stsSet = metaSet(s.StatefulSets)
	s.rsSet = metaSet(s.ReplicaSets)

	// A service "has ready endpoints" when at least one of its EndpointSlices
	// contains an endpoint whose Ready condition is true (nil means ready,
	// per the EndpointSlice API contract).
	s.readyEP = make(map[string]struct{})
	for i := range s.EndpointSlices {
		es := &s.EndpointSlices[i]
		svcName := es.Labels[discoveryv1.LabelServiceName]
		if svcName == "" {
			continue
		}
		k := key(es.Namespace, svcName)
		if _, done := s.readyEP[k]; done {
			continue
		}
		for _, ep := range es.Endpoints {
			if len(ep.Addresses) == 0 {
				continue
			}
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				s.readyEP[k] = struct{}{}
				break
			}
		}
	}
}

func metaSet(objs []metav1.PartialObjectMetadata) map[string]struct{} {
	set := make(map[string]struct{}, len(objs))
	for i := range objs {
		set[key(objs[i].Namespace, objs[i].Name)] = struct{}{}
	}
	return set
}

// GetService returns the Service ns/name or nil.
func (s *Snapshot) GetService(ns, name string) *corev1.Service {
	s.index()
	return s.svcIndex[key(ns, name)]
}

// GetPod returns the Pod ns/name or nil.
func (s *Snapshot) GetPod(ns, name string) *corev1.Pod {
	s.index()
	return s.podIndex[key(ns, name)]
}

// PodsInNamespace returns the pods of one namespace.
func (s *Snapshot) PodsInNamespace(ns string) []*corev1.Pod {
	s.index()
	return s.podsByNS[ns]
}

// HasWorkload reports whether a Deployment/StatefulSet/ReplicaSet ns/name
// exists in the snapshot. Unknown kinds return false; callers are expected
// to filter kinds before calling.
func (s *Snapshot) HasWorkload(kind, ns, name string) bool {
	s.index()
	var set map[string]struct{}
	switch kind {
	case "Deployment":
		set = s.deploySet
	case "StatefulSet":
		set = s.stsSet
	case "ReplicaSet":
		set = s.rsSet
	default:
		return false
	}
	_, ok := set[key(ns, name)]
	return ok
}

// ServiceHasReadyEndpoints reports whether the Service ns/name is backed by
// at least one ready endpoint.
func (s *Snapshot) ServiceHasReadyEndpoints(ns, name string) bool {
	s.index()
	_, ok := s.readyEP[key(ns, name)]
	return ok
}

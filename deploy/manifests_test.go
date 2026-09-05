// Package deploy holds the raw manifests. This test keeps them honest: they
// must decode strictly into the Kubernetes API types (no typos, no unknown
// fields), the ClusterRole must grant exactly what the checks declare they
// need, and the CronJob must keep its hardening.
package deploy

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/alessandrocorsico/oskar/internal/checks"
	"github.com/alessandrocorsico/oskar/internal/cluster"
)

func decodeAll(t *testing.T, path string) []runtime.Object {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	strict := serializer.NewCodecFactory(scheme.Scheme, serializer.EnableStrict).UniversalDeserializer()
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var objs []runtime.Object
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return objs
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		obj, _, err := strict.Decode(doc, nil, nil)
		if err != nil {
			t.Fatalf("%s does not decode strictly: %v", path, err)
		}
		objs = append(objs, obj)
	}
}

func manifests(t *testing.T) (ns *corev1.Namespace, sa *corev1.ServiceAccount, role *rbacv1.ClusterRole, binding *rbacv1.ClusterRoleBinding, cj *batchv1.CronJob) {
	t.Helper()
	objs := append(decodeAll(t, "rbac.yaml"), decodeAll(t, "cronjob.yaml")...)
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Namespace:
			ns = v
		case *corev1.ServiceAccount:
			sa = v
		case *rbacv1.ClusterRole:
			role = v
		case *rbacv1.ClusterRoleBinding:
			binding = v
		case *batchv1.CronJob:
			cj = v
		default:
			t.Fatalf("unexpected object %T in deploy/", o)
		}
	}
	if ns == nil || sa == nil || role == nil || binding == nil || cj == nil {
		t.Fatalf("deploy/ must ship Namespace, ServiceAccount, ClusterRole, ClusterRoleBinding and CronJob")
	}
	return ns, sa, role, binding, cj
}

// apiGroupOf maps snapshot resources to the API group of their RBAC rule.
var apiGroupOf = map[cluster.Resource]string{
	cluster.ResourcePods:                   "",
	cluster.ResourceServices:               "",
	cluster.ResourceTLSSecrets:             "",
	cluster.ResourcePersistentVolumes:      "",
	cluster.ResourcePersistentVolumeClaims: "",
	cluster.ResourceNamespaces:             "",
	cluster.ResourceEndpointSlices:         "discovery.k8s.io",
	cluster.ResourceIngresses:              "networking.k8s.io",
	cluster.ResourceValidatingWebhooks:     "admissionregistration.k8s.io",
	cluster.ResourceMutatingWebhooks:       "admissionregistration.k8s.io",
	cluster.ResourceHPAs:                   "autoscaling",
	cluster.ResourceDeployments:            "apps",
	cluster.ResourceStatefulSets:           "apps",
	cluster.ResourceReplicaSets:            "apps",
	cluster.ResourceRoutes:                 "route.openshift.io",
}

func TestWiring(t *testing.T) {
	ns, sa, role, binding, cj := manifests(t)
	if sa.Namespace != ns.Name || cj.Namespace != ns.Name {
		t.Fatalf("ServiceAccount and CronJob must live in namespace %q", ns.Name)
	}
	if cj.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName != sa.Name {
		t.Fatalf("CronJob must run as ServiceAccount %q", sa.Name)
	}
	if binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 ||
		binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != sa.Name || binding.Subjects[0].Namespace != ns.Name {
		t.Fatalf("ClusterRoleBinding must bind %q to %s/%s", role.Name, ns.Name, sa.Name)
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatal("the oskar namespace should enforce the restricted Pod Security Standard")
	}
}

func TestClusterRoleGrantsExactlyWhatChecksNeed(t *testing.T) {
	_, _, role, _, _ := manifests(t)

	granted := map[string]bool{} // "group/resource"
	for _, rule := range role.Rules {
		if len(rule.Verbs) != 1 || rule.Verbs[0] != "list" {
			t.Errorf("rule %v: oskar only ever lists; verbs must be exactly [list]", rule.Resources)
		}
		for _, g := range rule.APIGroups {
			for _, r := range rule.Resources {
				granted[g+"/"+r] = true
			}
		}
	}

	needed := map[string]bool{}
	for _, n := range checks.Needed(checks.All()) {
		group, ok := apiGroupOf[n.Resource]
		if !ok {
			t.Fatalf("resource %s has no API group mapping in this test; add it", n.Resource)
		}
		key := group + "/" + string(n.Resource)
		needed[key] = true
		if !granted[key] {
			t.Errorf("checks need %s but deploy/rbac.yaml does not grant list on it", key)
		}
	}
	for key := range granted {
		if !needed[key] {
			t.Errorf("deploy/rbac.yaml grants %s but no check needs it (least privilege)", key)
		}
	}
}

func TestCronJobStaysHardened(t *testing.T) {
	_, _, _, _, cj := manifests(t)
	spec := cj.Spec

	if spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Error("concurrencyPolicy must be Forbid: overlapping scans are pointless")
	}
	if spec.StartingDeadlineSeconds == nil {
		t.Error("startingDeadlineSeconds missing")
	}
	if spec.TimeZone == nil || *spec.TimeZone == "" {
		t.Error("timeZone missing: the schedule would silently run in the controller-manager's zone")
	}
	job := spec.JobTemplate.Spec
	if job.ActiveDeadlineSeconds == nil {
		t.Error("activeDeadlineSeconds missing: with Forbid, one hung scan would block every future run")
	}
	if job.TTLSecondsAfterFinished == nil {
		t.Error("ttlSecondsAfterFinished missing")
	}

	pod := job.Template.Spec
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Error("pod must run as non-root")
	}
	if pod.SecurityContext == nil || pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("seccompProfile RuntimeDefault missing: rejected by the restricted Pod Security Standard")
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("expected one container, got %d", len(pod.Containers))
	}
	c := pod.Containers[0]
	if strings.HasSuffix(c.Image, ":latest") || !strings.Contains(c.Image, ":") {
		t.Errorf("image %q must be pinned to a release tag", c.Image)
	}
	if c.SecurityContext == nil ||
		c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem ||
		c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation ||
		c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Error("container must be read-only, non-escalating and drop all capabilities")
	}
	if c.Resources.Limits.Memory().IsZero() {
		t.Error("memory limit missing")
	}
	args := strings.Join(c.Args, " ")
	if !strings.Contains(args, "--request-timeout") {
		t.Error("the in-cluster scan should set --request-timeout so a hung API call cannot outlive the Job deadline")
	}
	for _, labels := range []map[string]string{cj.Labels, spec.JobTemplate.Labels, job.Template.Labels} {
		if labels["app.kubernetes.io/name"] != "oskar" {
			t.Error("app.kubernetes.io/name=oskar label missing (needed to select the Job in kube-state-metrics alerts)")
		}
	}
}

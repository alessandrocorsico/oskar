// Package cluster resolves the client configuration and builds the
// read-only Snapshot the checks operate on.
package cluster

// Resource identifies a kind of object the Snapshot knows how to list.
//
// Checks declare what they read through Need values; BuildSnapshot fetches
// only the union of what the selected checks need, so an unselected check
// costs neither an API call nor an RBAC permission.
type Resource string

// Resources the snapshot can hold. The values double as the plural names
// used in notices and in deploy/rbac.yaml.
const (
	ResourcePods                   Resource = "pods"
	ResourceServices               Resource = "services"
	ResourceEndpointSlices         Resource = "endpointslices"
	ResourceIngresses              Resource = "ingresses"
	ResourceValidatingWebhooks     Resource = "validatingwebhookconfigurations"
	ResourceMutatingWebhooks       Resource = "mutatingwebhookconfigurations"
	ResourceTLSSecrets             Resource = "secrets"
	ResourcePersistentVolumes      Resource = "persistentvolumes"
	ResourcePersistentVolumeClaims Resource = "persistentvolumeclaims"
	ResourceNamespaces             Resource = "namespaces"
	ResourceHPAs                   Resource = "horizontalpodautoscalers"
	ResourceDeployments            Resource = "deployments"
	ResourceStatefulSets           Resource = "statefulsets"
	ResourceReplicaSets            Resource = "replicasets"
	ResourceRoutes                 Resource = "routes"
)

// Need declares one resource a check reads.
//
// ClusterWide asks for the resource to be listed across all namespaces even
// when the scan is restricted with --namespace: cluster-scoped objects such
// as webhook configurations reference Services anywhere in the cluster.
//
// Optional marks a resource the check can do without. The snapshot still
// tries to fetch it, but the check runs even when it could not be listed
// and must consult Snapshot.Available before relying on it.
type Need struct {
	Resource    Resource
	ClusterWide bool
	Optional    bool
}

// Needs builds plain, required, namespace-scoped needs.
func Needs(rs ...Resource) []Need {
	out := make([]Need, 0, len(rs))
	for _, r := range rs {
		out = append(out, Need{Resource: r})
	}
	return out
}

// namespaced lists the resources that live inside a namespace. The others
// are cluster-scoped and are always listed cluster-wide, whatever the
// --namespace flag says.
var namespaced = map[Resource]bool{
	ResourcePods:                   true,
	ResourceServices:               true,
	ResourceEndpointSlices:         true,
	ResourceIngresses:              true,
	ResourceTLSSecrets:             true,
	ResourcePersistentVolumeClaims: true,
	ResourceHPAs:                   true,
	ResourceDeployments:            true,
	ResourceStatefulSets:           true,
	ResourceReplicaSets:            true,
	ResourceRoutes:                 true,
}

package cluster

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	// Auth provider plugins (OIDC) plus built-in exec credential support, so
	// that EKS (aws eks get-token), AKS (kubelogin), GKE
	// (gke-gcloud-auth-plugin) and OpenShift kubeconfigs work out of the box.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

// ConfigOptions controls how the client configuration is resolved.
type ConfigOptions struct {
	Kubeconfig string        // explicit path; empty means the kubectl default chain
	Context    string        // kubeconfig context override
	Timeout    time.Duration // per-request timeout; zero means none
	UserAgent  string
}

// Connection is a resolved client configuration plus the identity of the
// cluster it points at, for the scan report.
type Connection struct {
	Config  *rest.Config
	Context string // kubeconfig context in use; empty when running in-cluster
	Server  string
}

// BuildConfig resolves a client configuration the way kubectl does:
// explicit --kubeconfig > $KUBECONFIG > ~/.kube/config, honoring --context,
// and falls back to in-cluster configuration only when no kubeconfig
// content exists at all.
//
// Any other failure (a malformed file, an unknown --context, a missing
// explicit --kubeconfig) is returned as an error: oskar must never silently
// scan a different cluster than the one it was asked to scan.
func BuildConfig(o ConfigOptions) (*Connection, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.Kubeconfig != "" {
		rules.ExplicitPath = o.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: o.Context}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	// The deferred loader itself falls back to in-cluster configuration when
	// the merged kubeconfig is empty and a service account token is mounted.
	cfg, err := loader.ClientConfig()
	if err != nil {
		if clientcmd.IsEmptyConfig(err) {
			return nil, errors.New("no kubeconfig found (--kubeconfig, $KUBECONFIG, ~/.kube/config) and not running in-cluster")
		}
		return nil, err
	}

	conn := &Connection{Config: tune(cfg, o), Server: cfg.Host}
	if raw, rerr := loader.RawConfig(); rerr == nil {
		conn.Context = raw.CurrentContext
	}
	if o.Context != "" {
		conn.Context = o.Context
	}
	return conn, nil
}

func tune(cfg *rest.Config, o ConfigOptions) *rest.Config {
	cfg.QPS = 50
	cfg.Burst = 100
	cfg.UserAgent = o.UserAgent
	cfg.Timeout = o.Timeout
	// Protobuf cuts the wire size of large List responses (pods above all)
	// several-fold and is cheaper to decode. The dynamic client used for
	// Routes and the discovery client negotiate JSON on their own.
	cfg.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	cfg.ContentType = "application/vnd.kubernetes.protobuf"
	return cfg
}

// Clients bundles the API clients the snapshot builder uses. All three are
// interfaces so tests can plug in the client-go fakes.
type Clients struct {
	Kube     kubernetes.Interface
	Dynamic  dynamic.Interface  // OpenShift Routes (unstructured)
	Metadata metadata.Interface // workloads, metadata only
}

// NewClients builds the real clients from a resolved configuration.
func NewClients(cfg *rest.Config) (Clients, error) {
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Clients{}, fmt.Errorf("creating kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return Clients{}, fmt.Errorf("creating dynamic client: %w", err)
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return Clients{}, fmt.Errorf("creating metadata client: %w", err)
	}
	return Clients{Kube: kube, Dynamic: dyn, Metadata: meta}, nil
}

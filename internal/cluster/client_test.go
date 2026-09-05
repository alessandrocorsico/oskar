package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: prod
  cluster:
    server: https://prod.example.invalid:6443
    insecure-skip-tls-verify: true
- name: staging
  cluster:
    server: https://staging.example.invalid:6443
    insecure-skip-tls-verify: true
users:
- name: ci
  user:
    token: not-a-real-token
contexts:
- name: prod
  context:
    cluster: prod
    user: ci
- name: staging
  context:
    cluster: staging
    user: ci
current-context: prod
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// isolate makes sure neither the developer's kubeconfig nor an in-cluster
// environment leaks into the test.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBECONFIG", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
}

func TestBuildConfigResolvesContextAndServer(t *testing.T) {
	isolate(t)
	conn, err := BuildConfig(ConfigOptions{Kubeconfig: writeKubeconfig(t), Timeout: 7 * time.Second, UserAgent: "oskar/test"})
	if err != nil {
		t.Fatal(err)
	}
	if conn.Context != "prod" || conn.Server != "https://prod.example.invalid:6443" {
		t.Fatalf("unexpected connection %+v", conn)
	}
	cfg := conn.Config
	if cfg.Timeout != 7*time.Second || cfg.UserAgent != "oskar/test" {
		t.Fatalf("timeout/user-agent not applied: %+v", cfg)
	}
	if cfg.ContentType != "application/vnd.kubernetes.protobuf" || !strings.Contains(cfg.AcceptContentTypes, "protobuf") {
		t.Fatalf("protobuf content negotiation not applied: %q / %q", cfg.ContentType, cfg.AcceptContentTypes)
	}
}

func TestBuildConfigHonorsContextOverride(t *testing.T) {
	isolate(t)
	conn, err := BuildConfig(ConfigOptions{Kubeconfig: writeKubeconfig(t), Context: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if conn.Context != "staging" || conn.Server != "https://staging.example.invalid:6443" {
		t.Fatalf("unexpected connection %+v", conn)
	}
}

func TestBuildConfigUnknownContextIsAnError(t *testing.T) {
	isolate(t)
	_, err := BuildConfig(ConfigOptions{Kubeconfig: writeKubeconfig(t), Context: "nope"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("an unknown --context must fail loudly, got %v", err)
	}
}

func TestBuildConfigMissingExplicitFileIsAnError(t *testing.T) {
	isolate(t)
	_, err := BuildConfig(ConfigOptions{Kubeconfig: filepath.Join(t.TempDir(), "missing")})
	if err == nil || strings.Contains(err.Error(), "not running in-cluster") {
		t.Fatalf("a missing explicit --kubeconfig must not fall back to in-cluster, got %v", err)
	}
}

func TestBuildConfigNothingAvailable(t *testing.T) {
	isolate(t)
	_, err := BuildConfig(ConfigOptions{})
	if err == nil || !strings.Contains(err.Error(), "no kubeconfig found") {
		t.Fatalf("expected the friendly no-kubeconfig error, got %v", err)
	}
}

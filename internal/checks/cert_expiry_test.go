package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

var certNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func makeTLSSecret(t *testing.T, ns, name string, notAfter time.Time) corev1.Secret {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.local"},
		NotBefore:    certNow.Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: crt},
	}
}

func runCert(t *testing.T, secrets []corev1.Secret, warnDays int) []Finding {
	t.Helper()
	snap := &cluster.Snapshot{TLSSecrets: secrets}
	fs, err := (&CertExpiry{}).Run(context.Background(), snap, Options{CertWarnDays: warnDays, Now: certNow})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestCertExpiryExpired(t *testing.T) {
	fs := runCert(t, []corev1.Secret{makeTLSSecret(t, "default", "old-cert", certNow.Add(-24*time.Hour))}, 30)
	if len(fs) != 1 || fs[0].Severity != SeverityCritical {
		t.Fatalf("expected 1 CRITICAL finding, got %+v", fs)
	}
	if fs[0].Kind != "Secret" || fs[0].Name != "old-cert" || !strings.Contains(fs[0].Message, "expired on 2026-09-04") {
		t.Fatalf("unexpected finding %+v", fs[0])
	}
}

func TestCertExpirySoon(t *testing.T) {
	fs := runCert(t, []corev1.Secret{makeTLSSecret(t, "default", "soon-cert", certNow.Add(5*24*time.Hour))}, 30)
	if len(fs) != 1 || fs[0].Severity != SeverityWarning {
		t.Fatalf("expected 1 WARNING finding, got %+v", fs)
	}
	if !strings.Contains(fs[0].Message, "expires in 5 days") {
		t.Fatalf("unexpected message %q", fs[0].Message)
	}
}

func TestCertExpiryOK(t *testing.T) {
	fs := runCert(t, []corev1.Secret{makeTLSSecret(t, "default", "fresh-cert", certNow.Add(90*24*time.Hour))}, 30)
	if len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %+v", fs)
	}
}

func TestCertExpiryWarnWindowZeroDisablesWarning(t *testing.T) {
	fs := runCert(t, []corev1.Secret{makeTLSSecret(t, "default", "soon-cert", certNow.Add(24*time.Hour))}, 0)
	if len(fs) != 0 {
		t.Fatalf("expected 0 findings with a zero warn window, got %+v", fs)
	}
}

func TestCertExpiryCorruptSecrets(t *testing.T) {
	empty := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"}, Type: corev1.SecretTypeTLS}
	notPEM := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "garbage", Namespace: "default"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("not a certificate")},
	}
	fs := runCert(t, []corev1.Secret{empty, notPEM}, 30)
	if len(fs) != 2 {
		t.Fatalf("expected 2 findings, got %+v", fs)
	}
	for _, f := range fs {
		if f.Severity != SeverityWarning {
			t.Fatalf("corrupt secrets are WARNING, got %+v", f)
		}
	}
}

func TestCertExpiryIgnoreAnnotation(t *testing.T) {
	sec := makeTLSSecret(t, "default", "old-cert", certNow.Add(-24*time.Hour))
	annotateIgnore(&sec.ObjectMeta, "cert-expiry")
	if fs := runCert(t, []corev1.Secret{sec}, 30); len(fs) != 0 {
		t.Fatalf("annotated secret must be skipped, got %+v", fs)
	}
}

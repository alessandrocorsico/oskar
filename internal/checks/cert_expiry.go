package checks

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/alessandrocorsico/oskar/internal/cluster"
)

// CertExpiry inspects kubernetes.io/tls Secrets and flags certificates that
// are expired or expiring soon. Only TLS-typed secrets are ever fetched from
// the API (server-side field selector) and the private key is discarded by
// the snapshot before any check runs.
type CertExpiry struct{}

// Name implements Check.
func (c *CertExpiry) Name() string { return "cert-expiry" }

// Description implements Check.
func (c *CertExpiry) Description() string {
	return "TLS Secrets with expired or soon-to-expire certificates"
}

// Needs implements Check.
func (c *CertExpiry) Needs() []cluster.Need {
	return cluster.Needs(cluster.ResourceTLSSecrets)
}

// Run implements Check.
func (c *CertExpiry) Run(_ context.Context, snap *cluster.Snapshot, opts Options) ([]Finding, error) {
	now := opts.now()
	warnWindow := time.Duration(opts.CertWarnDays) * 24 * time.Hour
	var out []Finding
	for i := range snap.TLSSecrets {
		sec := &snap.TLSSecrets[i]
		if ignored(sec, c.Name()) {
			continue
		}
		f := Finding{
			Check:     c.Name(),
			Severity:  SeverityWarning,
			Namespace: sec.Namespace,
			Kind:      "Secret",
			Name:      sec.Name,
		}
		raw := sec.Data[corev1.TLSCertKey]
		if len(raw) == 0 {
			f.Message = "TLS Secret has an empty tls.crt"
			f.Hint = "whatever consumes this secret will fail its TLS handshake; re-issue the certificate or delete the secret"
			out = append(out, f)
			continue
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			f.Message = "tls.crt is not valid PEM"
			f.Hint = "the secret content is corrupted; re-issue the certificate"
			out = append(out, f)
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			f.Message = fmt.Sprintf("could not parse certificate: %v", err)
			f.Hint = "the secret content is corrupted; re-issue the certificate"
			out = append(out, f)
			continue
		}
		subject := cert.Subject.CommonName
		if subject == "" && len(cert.DNSNames) > 0 {
			subject = cert.DNSNames[0]
		}
		if subject == "" {
			subject = "certificate"
		}
		switch {
		case cert.NotAfter.Before(now):
			f.Severity = SeverityCritical
			f.Message = fmt.Sprintf("%s expired on %s (%d days ago)",
				subject, cert.NotAfter.Format("2006-01-02"), int(now.Sub(cert.NotAfter).Hours()/24))
			f.Hint = "anything terminating TLS with this secret is serving an expired certificate right now; renew it (cert-manager, ACME, or manual re-issue)"
			out = append(out, f)
		case cert.NotAfter.Sub(now) < warnWindow:
			f.Message = fmt.Sprintf("%s expires in %d days (on %s)",
				subject, int(cert.NotAfter.Sub(now).Hours()/24), cert.NotAfter.Format("2006-01-02"))
			f.Hint = "renew before expiry; if cert-manager should be handling this, inspect its Certificate and Order resources"
			out = append(out, f)
		}
	}
	return out, nil
}

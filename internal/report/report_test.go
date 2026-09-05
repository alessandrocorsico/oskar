package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alessandrocorsico/oskar/internal/checks"
)

func sampleOutput() Output {
	findings := []checks.Finding{
		{Check: "dead-webhook", Severity: checks.SeverityCritical, Kind: "ValidatingWebhookConfiguration", Name: "policy",
			Related: []checks.Ref{{Kind: "Service", Namespace: "policy", Name: "policy-svc"}}, Message: "points nowhere", Hint: "fix it", Fingerprint: "abc"},
		{Check: "service-selector", Severity: checks.SeverityWarning, Namespace: "default", Kind: "Service", Name: "web",
			Message: "no pods"},
		{Check: "x", Severity: checks.SeverityInfo, Namespace: "default", Kind: "Pod", Name: "p", Message: "fyi"},
	}
	statuses := []checks.Status{
		{Name: "dead-webhook", State: checks.StateOK, Findings: 1},
		{Name: "ghost-endpoints", State: checks.StateSkipped, Reason: "pods could not be listed"},
		{Name: "broken", State: checks.StateFailed, Reason: "boom"},
	}
	return New(findings, []string{"could not list pods"}, Scan{OskarVersion: "v1.2.3", Context: "prod", Server: "https://x", Checks: statuses}, 1500*time.Millisecond)
}

func TestNewSummarizes(t *testing.T) {
	out := sampleOutput()
	s := out.Summary
	if s.Critical != 1 || s.Warning != 1 || s.Info != 1 || s.Total != 3 {
		t.Fatalf("unexpected counts %+v", s)
	}
	if s.ChecksRun != 1 || s.ChecksSkipped != 1 || s.ChecksFailed != 1 || s.DurationMS != 1500 {
		t.Fatalf("unexpected check counts %+v", s)
	}
	if out.SchemaVersion != SchemaVersion || out.GeneratedAt.IsZero() {
		t.Fatalf("schema/timestamp missing: %+v", out)
	}

	empty := New(nil, nil, Scan{}, 0)
	if empty.Findings == nil || empty.Scan.Checks == nil {
		t.Fatal("nil slices must be rendered as empty JSON arrays, not null")
	}
}

func TestWriteJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleOutput()); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"schemaVersion", "generatedAt", "scan", "summary", "findings", "notices"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("top-level key %q missing", key)
		}
	}
	text := buf.String()
	for _, want := range []string{`"schemaVersion": "1"`, `"oskarVersion": "v1.2.3"`, `"kind": "ValidatingWebhookConfiguration"`, `"related"`, `"fingerprint": "abc"`, `"state": "skipped"`} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %s in output:\n%s", want, text)
		}
	}
	if strings.Contains(text, `"key"`) {
		t.Error("Key is internal and must not be serialized")
	}
}

func TestWriteTable(t *testing.T) {
	var buf bytes.Buffer
	WriteTable(&buf, sampleOutput(), false)
	text := buf.String()
	for _, want := range []string{
		"[CRITICAL] dead-webhook  ValidatingWebhookConfiguration/policy (Service/policy/policy-svc)",
		"[WARNING]  service-selector  default/Service/web",
		"hint: fix it",
		"! could not list pods",
		"1 critical, 1 warning, 1 info",
		"3 findings from 1 checks (1 skipped, 1 failed)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in table output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\033[") {
		t.Error("no ANSI codes expected with color off")
	}

	buf.Reset()
	WriteTable(&buf, New(nil, nil, Scan{Checks: []checks.Status{{Name: "a", State: checks.StateOK}}}, time.Second), true)
	if !strings.Contains(buf.String(), "No anomalies found") || !strings.Contains(buf.String(), "\033[32m") {
		t.Fatalf("unexpected clean output:\n%s", buf.String())
	}
}

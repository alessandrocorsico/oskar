// Package report renders scan results for machines (JSON, the contract) and
// for humans (the table).
package report

import (
	"encoding/json"
	"io"
	"time"

	"github.com/alessandrocorsico/oskar/internal/checks"
)

// SchemaVersion is bumped on every incompatible change to Output.
const SchemaVersion = "1"

// Output is the machine-readable scan result. It is the contract for any
// renderer (web dashboard, Lens extension, Prometheus exporter): the CLI
// table is just the first consumer of this structure.
type Output struct {
	SchemaVersion string           `json:"schemaVersion"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	Scan          Scan             `json:"scan"`
	Summary       Summary          `json:"summary"`
	Findings      []checks.Finding `json:"findings"`
	Notices       []string         `json:"notices,omitempty"`
}

// Scan identifies what was scanned, by which oskar, and how each check
// fared, so a report can be interpreted without the command line that
// produced it.
type Scan struct {
	OskarVersion string          `json:"oskarVersion"`
	Context      string          `json:"context,omitempty"`
	Server       string          `json:"server,omitempty"`
	Namespace    string          `json:"namespace,omitempty"` // empty means all namespaces
	Checks       []checks.Status `json:"checks"`
}

// Summary aggregates finding counts and scan metadata.
type Summary struct {
	Critical      int   `json:"critical"`
	Warning       int   `json:"warning"`
	Info          int   `json:"info"`
	Total         int   `json:"total"`
	ChecksRun     int   `json:"checksRun"`
	ChecksSkipped int   `json:"checksSkipped"`
	ChecksFailed  int   `json:"checksFailed"`
	DurationMS    int64 `json:"durationMs"`
}

// New assembles the scan result.
func New(findings []checks.Finding, notices []string, scan Scan, elapsed time.Duration) Output {
	if findings == nil {
		findings = []checks.Finding{}
	}
	if scan.Checks == nil {
		scan.Checks = []checks.Status{}
	}
	return Output{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Scan:          scan,
		Summary:       summarize(findings, scan.Checks, elapsed),
		Findings:      findings,
		Notices:       notices,
	}
}

// WriteJSON encodes the scan result as indented JSON.
func WriteJSON(w io.Writer, out Output) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func summarize(fs []checks.Finding, statuses []checks.Status, elapsed time.Duration) Summary {
	s := Summary{DurationMS: elapsed.Milliseconds(), Total: len(fs)}
	for _, f := range fs {
		switch f.Severity {
		case checks.SeverityCritical:
			s.Critical++
		case checks.SeverityWarning:
			s.Warning++
		default:
			s.Info++
		}
	}
	for _, st := range statuses {
		switch st.State {
		case checks.StateOK:
			s.ChecksRun++
		case checks.StateSkipped:
			s.ChecksSkipped++
		case checks.StateFailed:
			s.ChecksFailed++
		}
	}
	return s
}

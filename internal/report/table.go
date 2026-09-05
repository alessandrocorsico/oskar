package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/alessandrocorsico/oskar/internal/checks"
)

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiGreen  = "\033[32m"
)

func paint(s, code string, on bool) string {
	if !on {
		return s
	}
	return code + s + ansiReset
}

func severityTag(sev checks.Severity, color bool) string {
	tag := fmt.Sprintf("%-10s", "["+string(sev)+"]")
	switch sev {
	case checks.SeverityCritical:
		return paint(tag, ansiRed+ansiBold, color)
	case checks.SeverityWarning:
		return paint(tag, ansiYellow, color)
	default:
		return paint(tag, ansiCyan, color)
	}
}

// objectLine renders "ns/Kind/name (Related/...)" for humans.
func objectLine(f checks.Finding) string {
	obj := f.Object()
	if f.Namespace != "" {
		obj = f.Namespace + "/" + obj
	}
	if len(f.Related) == 0 {
		return obj
	}
	rel := make([]string, 0, len(f.Related))
	for _, r := range f.Related {
		if r.Namespace == f.Namespace {
			rel = append(rel, r.Kind+"/"+r.Name)
		} else {
			rel = append(rel, r.String())
		}
	}
	return obj + " (" + strings.Join(rel, ", ") + ")"
}

// WriteTable renders the scan result for humans: one block per finding with
// the message and a remediation hint, followed by notices and a summary line.
func WriteTable(w io.Writer, out Output, color bool) {
	for _, f := range out.Findings {
		fmt.Fprintf(w, "%s %s  %s\n", severityTag(f.Severity, color), paint(f.Check, ansiBold, color), objectLine(f))
		fmt.Fprintf(w, "    %s\n", f.Message)
		if f.Hint != "" {
			fmt.Fprintf(w, "    %s\n", paint("hint: "+f.Hint, ansiDim, color))
		}
		fmt.Fprintln(w)
	}

	for _, n := range out.Notices {
		fmt.Fprintf(w, "%s %s\n", paint("!", ansiYellow, color), n)
	}
	if len(out.Notices) > 0 {
		fmt.Fprintln(w)
	}

	s := out.Summary
	elapsed := time.Duration(s.DurationMS) * time.Millisecond
	checksNote := fmt.Sprintf("%d checks", s.ChecksRun)
	if s.ChecksSkipped > 0 || s.ChecksFailed > 0 {
		checksNote += fmt.Sprintf(" (%d skipped, %d failed)", s.ChecksSkipped, s.ChecksFailed)
	}
	if len(out.Findings) == 0 {
		fmt.Fprintf(w, "%s No anomalies found — %s passed in %s.\n",
			paint("OK", ansiGreen+ansiBold, color), checksNote, elapsed)
		return
	}
	fmt.Fprintf(w, "%s: %s, %s, %s — %d findings from %s in %s\n",
		paint("Summary", ansiBold, color),
		paint(fmt.Sprintf("%d critical", s.Critical), ansiRed, color),
		paint(fmt.Sprintf("%d warning", s.Warning), ansiYellow, color),
		paint(fmt.Sprintf("%d info", s.Info), ansiCyan, color),
		len(out.Findings), checksNote, elapsed)
}

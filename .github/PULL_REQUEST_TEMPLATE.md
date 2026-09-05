<!-- Title as a conventional commit: feat: ..., fix: ..., docs: ..., chore: ... -->

## What

<!-- One paragraph: what changes and why. Link the issue if there is one. -->

## Checklist

- [ ] `make verify test lint` is green locally
- [ ] New or changed check: `Needs()` declares every resource it reads, and
      `deploy/rbac.yaml` grants it (`go test ./deploy/` enforces this)
- [ ] New or changed check: tests cover the positive case, the healthy case,
      the `oskar.io/ignore` annotation and every branch that avoids a false
      positive
- [ ] README check table and `CONTRIBUTING.md` updated if user-visible
- [ ] JSON output changed incompatibly → `report.SchemaVersion` bumped

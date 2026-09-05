# Contributing to OSKAR

Thanks for considering a contribution! OSKAR is intentionally small and
boring on the inside so that adding value is easy.

## Dev setup

```bash
git clone https://github.com/alessandrocorsico/oskar.git
cd oskar
make build       # bin/oskar (go.sum is committed: no tidy needed)
make test        # go test -race ./...
make lint        # golangci-lint (pinned version, via go run)
make vuln        # govulncheck
make verify      # what CI enforces: go.sum verified, tidy is a no-op, gofmt clean
```

CI runs build, vet, race tests, gofmt, golangci-lint, govulncheck, a
`go mod tidy` drift check and kubeconform on `deploy/` for every PR.

## Adding a check (the most valuable contribution)

1. Create `internal/checks/<name>.go` with a struct implementing the
   `Check` interface: `Name`, `Description`, `Needs`, `Run`.
2. `Needs` declares the snapshot resources the check reads
   (`cluster.Need`). Mark a need `ClusterWide` when the check follows
   references from cluster-scoped objects (webhooks → Services) so it keeps
   working under `--namespace`; mark it `Optional` when the check can do
   without it (then test `snap.Available(...)` before using the data). The
   engine fetches only what the selected checks need and **skips a check
   whose required resources could not be listed** instead of running it on
   empty data, so a check never has to wonder whether an empty slice means
   "nothing there" or "not allowed to look".
3. `Run` must be a **pure function over the Snapshot**: no API calls, no
   side effects, no `time.Now()` (use `opts.now()`). If you need a resource
   the Snapshot doesn't fetch yet, add it to `internal/cluster/resources.go`
   and `snapshot.go` (through the `fetch` helper so RBAC denials degrade
   into notices) and to `deploy/rbac.yaml`. Existence-only lookups should
   use the metadata client, like the workloads do.
4. Fill a `Finding` with `Kind`, `Name`, `Namespace` (if any), `Related`
   references, a `Message` (what is broken, one sentence) and a `Hint`
   (what a tired on-call human should do about it). Set `Key` when one
   object can produce several findings (an endpoint address, a webhook
   name, an ingress path): it keeps fingerprints unique.
5. Honor the `oskar.io/ignore` annotation with `ignored(obj, c.Name())`.
6. Severity guideline: `CRITICAL` = user-facing traffic broken or cluster
   operations blocked right now; `WARNING` = latent risk or degraded state;
   `INFO` = hygiene.
7. Register the check in `checks.All()` and add a table entry in README.
8. Add a unit test built from plain structs (see any `*_test.go` in
   `internal/checks`): the positive case, the healthy case, the annotation,
   and every "skip" branch that avoids a false positive.

## Guidelines

- Read-only forever: OSKAR must never mutate cluster state.
- No distribution-specific client libraries; optional APIs (like OpenShift
  Routes) are probed via discovery and skipped when absent.
- Prefer zero false positives over extra coverage: a noisy radar gets
  turned off. Transient states (a pod starting, an endpoint not ready) are
  not anomalies.
- A scan that could not read its data must never look clean: RBAC denials
  become skipped checks with a notice, anything else aborts with exit 1.
- Keep dependencies minimal (currently: cobra, client-go and x/sync).
- The JSON output is a contract: bump `report.SchemaVersion` on
  incompatible changes.

## Reporting anomaly classes

Found a "silent broken link" pattern OSKAR doesn't catch yet? Open an issue
with the story — war stories are the best feature requests this project can
receive.

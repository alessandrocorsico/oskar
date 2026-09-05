# Security policy

OSKAR is a read-only scanner, but the identity it runs with can list
resources cluster-wide, and with the shipped RBAC that includes Secrets.
Treat its ServiceAccount token like any cluster-admin-adjacent credential
and read the "RBAC — read-only by design" section of the README before
deploying it.

## Supported versions

Only the latest release receives fixes. Please upgrade before reporting.

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's private vulnerability
reporting for this repository (Security tab → "Report a vulnerability"):

https://github.com/alessandrocorsico/oskar/security/advisories/new

Include the output of `oskar version`, the Kubernetes distribution and
version, and a way to reproduce. You will get an acknowledgement within
seven days. Fixes ship as a new release together with a GitHub Security
Advisory, and the release notes credit the reporter unless asked otherwise.

## What counts as a security issue

- OSKAR writing to or mutating anything in the cluster.
- Cluster data leaving the process through anything other than stdout and
  stderr, or private key material surviving in the snapshot.
- A false "all clear": a scan that could not read its data but exits 0.
- Release integrity: an archive or image that does not verify with cosign
  as documented in the README.

## Verifying releases

Every release ships `checksums.txt` signed with cosign (keyless, GitHub
OIDC) and a multi-arch image signed the same way. The exact commands are in
the README under "Install".

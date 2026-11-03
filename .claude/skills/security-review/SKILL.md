---
name: security-review
description: Mandatory security checklist to run before every push or pull request on Vesper. Covers secrets, dependencies, Go code, Kubernetes RBAC, isolation of the environments Vesper creates, input validation, container images, CI and supply chain, and the GitHub integration.
---

# Security review

Run this checklist before every `git push` and before marking a pull request
ready. If an item fails or you are unsure, stop and tell a maintainer. Do not
push.

Vesper creates workloads on behalf of users inside a shared cluster. Least
privilege and isolation are core requirements here, not extras.

## 1. Secrets and sensitive data

- [ ] No credentials, tokens, private keys, kubeconfigs, `.env` files or real
      hostnames in code, tests, fixtures, docs or commit history.
- [ ] You read `git diff --cached` and `git diff origin/main...HEAD` looking
      for accidental secrets.
- [ ] `gitleaks` reports nothing, if it is installed (see its README for the
      scan command).
- [ ] Logs, events, status conditions and error messages never contain secrets
      or user-supplied credentials.
- [ ] Test data is obviously fake.
- If you find a real secret: treat it as compromised, do not push, tell a
  maintainer so it can be rotated.

## 2. Dependencies

- [ ] `go mod tidy` leaves no diff and `go mod verify` passes.
- [ ] `govulncheck ./...` reports no affected vulnerabilities. Install with
      `go install golang.org/x/vuln/cmd/govulncheck@latest`. Informational
      findings (vulnerable code not called) are noted in the pull request.
- [ ] Every new dependency is justified in the pull request: license compatible
      with Apache-2.0, actively maintained, and really needed (prefer the
      standard library).
- [ ] No `replace` directive in `go.mod` pointing at a fork or a local path.

## 3. Go code

- [ ] `go vet ./...` and `golangci-lint run` are clean (with `gosec` enabled).
- [ ] `go test -race ./...` passes.
- [ ] Every error is handled or wrapped (`%w`). No panic on user input.
- [ ] No `InsecureSkipVerify`, no MD5 or SHA-1 for security purposes, and
      `crypto/rand` (not `math/rand`) for anything secret.
- [ ] No shell command or file path built from user input without validation.
- [ ] HTTP servers and clients set timeouts, and contexts are propagated.

## 4. Input validation and the API

- [ ] CRD fields are validated in the schema (`+kubebuilder:validation:`
      markers: enum, pattern, minimum, maximum, required).
- [ ] Any value used to derive a Kubernetes name (namespace, label) is validated
      as a DNS-1123 label and length-limited.
- [ ] Image references are validated, and TTL and resource values have enforced
      upper bounds.
- [ ] The controller never modifies or deletes an object it did not create:
      check the ownership label or owner reference first. This prevents
      hijacking or deleting someone else's namespace.

## 5. Kubernetes RBAC (the controller's own permissions)

- [ ] `+kubebuilder:rbac` markers list only the resources and verbs actually
      used. No `*` for resources, verbs or API groups. Never `cluster-admin`.
- [ ] Prefer namespaced `Role`; use `ClusterRole` only for what must be
      cluster-wide (for example, creating namespaces).
- [ ] Manifests are regenerated (`make manifests`) and the generated RBAC diff
      is reviewed.
- [ ] No access to `Secrets` unless a feature requires it.

## 6. Isolation of the environments Vesper creates

- [ ] Each environment namespace enforces the Pod Security `restricted`
      profile (label `pod-security.kubernetes.io/enforce: restricted`).
- [ ] Pods Vesper creates set `allowPrivilegeEscalation: false`,
      `runAsNonRoot: true`, `capabilities.drop: ["ALL"]` and
      `seccompProfile.type: RuntimeDefault`.
- [ ] No `privileged` container, no `hostNetwork`, `hostPID` or `hostIPC`, and
      no `hostPath` volume.
- [ ] A `ResourceQuota` (and a `LimitRange` where relevant) is applied to every
      environment.
- [ ] A default-deny `NetworkPolicy` is applied, with explicit allows only.
- [ ] ServiceAccount token auto-mounting is disabled unless required.
- A namespace is a soft boundary. Documentation must never promise hard
  multi-tenant isolation.

## 7. Container images and runtime

- [ ] Minimal base image (for example distroless), non-root user, no secrets in
      any layer.
- [ ] Release manifests pin images by digest.
- [ ] Metrics and health endpoints expose no secrets, and the metrics endpoint
      is protected or reachable only inside the cluster.

## 8. CI and supply chain (`.github/workflows`)

- [ ] Third-party actions are pinned to a full-length commit SHA taken from the
      action's own repository, with the version in a comment.
- [ ] Workflow `permissions:` default to read-only (`contents: read`), raised per
      job only when needed.
- [ ] No untrusted input (`github.event.pull_request.title`, branch names,
      issue text) is interpolated into a `run:` script. Pass it through `env:`.
- [ ] No `pull_request_target` or `workflow_run` workflow checks out or runs
      untrusted pull request code.
- [ ] Secrets are never echoed and are not exposed to pull requests from forks.

## 9. GitHub integration (from v0.2)

- [ ] Webhook payloads are authenticated (HMAC SHA-256 signature, compared in
      constant time) before being processed.
- [ ] The GitHub App has minimal permissions; tokens are short-lived and never
      logged.
- [ ] Pull request data (title, branch, author) is untrusted: validate it
      before using it in names, labels, URLs or commands.

## 10. Reporting

- Never report a vulnerability in a public issue or pull request. Follow
  `SECURITY.md` once it exists, or use a private GitHub security advisory.
- At the end of the review, write in the pull request: "Security review:
  passed", or list each exception with its justification.

## References

- [Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
- [GitHub Actions secure use reference](https://docs.github.com/en/actions/reference/security/secure-use)
- [Tutorial: find and fix vulnerable dependencies with govulncheck](https://go.dev/doc/tutorial/govulncheck)
- [The Kubebuilder Book](https://book.kubebuilder.io/)

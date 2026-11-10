# Security Policy

Vesper creates workloads on behalf of users inside a shared cluster, so we take
security reports seriously.

## Supported versions

Vesper is pre-alpha. Until the first release, only the `main` branch is
supported. After v0.1.0, security fixes are provided for the latest minor
release.

## Reporting a vulnerability

**Do not open a public issue, pull request or discussion for a vulnerability.**

Report it privately with GitHub's private vulnerability reporting: open the
**Security** tab of this repository, choose **Report a vulnerability**, and fill
in the form. Include:

- what the vulnerability is and which component it affects (operator, CRD,
  `vesperctl`, release artifacts, CI);
- the version or commit, and your Kubernetes version if relevant;
- steps to reproduce, or a proof of concept;
- the impact you expect (for example: reading another environment's data,
  escaping an environment, privilege escalation of the operator).

Please do not include real credentials or other people's data.

## What to expect

These are targets, not guarantees; Vesper is maintained by a small team.

| Step | Target |
| --- | --- |
| Acknowledgement of your report | 3 working days |
| First assessment (accepted, need more information, or declined) | 10 working days |
| Fix or mitigation for a confirmed issue | depends on severity; we keep you informed |

We follow coordinated disclosure: we ask you to keep the report private until a
fix is released. We publish a security advisory with the fix and credit you,
unless you prefer to stay anonymous.

## Scope

What Vesper guarantees, and what it does not, is described in the
[security model](docs/security.md).

In scope: the operator, the CRD and its validation, `vesperctl`, the manifests
and images we publish, and our CI and release pipeline.

Out of scope:

- Weaknesses in Kubernetes, in your container runtime or in the images you ask
  Vesper to run.
- Findings that need cluster-admin rights you already have.
- A namespace is a **soft** boundary: Vesper isolates environments with
  namespaces, quotas, Pod Security and network policies, but it does not claim
  hard multi-tenant isolation. Do not run mutually hostile tenants on one
  cluster because of Vesper.

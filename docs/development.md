# Development

## Prerequisites

| Tool | Used for | Tested with |
| --- | --- | --- |
| Go | building and testing | see `go.mod` |
| Docker | runs the kind nodes | any recent Docker Engine or Docker Desktop |
| [kind](https://kind.sigs.k8s.io/) | local Kubernetes cluster | v0.33.0 |
| kubectl | talking to the cluster | a version within one minor of the cluster |

## Local cluster

```sh
make kind-up      # create the cluster "vesper-dev" (does nothing if it exists)
kubectl --context kind-vesper-dev get nodes
make kind-down    # delete it (does nothing if it does not exist)
```

Use another name with `make kind-up KIND_CLUSTER=other`. The kubectl context is
always `kind-<name>`.

### Kubernetes version

The Kubernetes version is set by the node image in `hack/kind-config.yaml`. It
is pinned by tag and by digest so that every contributor and CI test the same
version. To upgrade, take the image listed in the release notes of the new kind
version and update the file and the table above.

## Tests

```sh
make test               # unit tests, no cluster needed
make test-integration   # vesperctl against the kind cluster (created if missing)
```

### Integration tests

The integration tests in `test/integration` sit behind the `integration` build
tag, so `go test ./...` skips them. They build the `vesperctl` binary and run
`list` and `get` against a real API server, with the stub CRD in
`test/integration/testdata`: same group, version and names as the real
`PreviewEnvironment` API, without validation. The stub will be replaced by the
generated CRD once the operator defines the API.

Each run creates the stub CRD, a namespace named `vesper-it-<random>` and a few
previews, and deletes them at the end, even when a test fails. Everything it
creates carries the label `app.kubernetes.io/managed-by=vesper-integration-tests`.

- If a `PreviewEnvironment` CRD that the tests did not create is installed
  (for example by the operator), it is left in place and the "not installed"
  cases are skipped.
- A stub CRD left by an interrupted run is removed before the tests start.
- The tests only run against a context named `kind-...`; they refuse any
  other cluster. `VESPER_IT_CONTEXT` selects the context, and
  `make test-integration KIND_CLUSTER=other` sets it for you.

CI runs them on every pull request, in the `integration` job.

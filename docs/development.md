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

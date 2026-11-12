# ADR-0005: Isolate each preview on the network

- **Status:** Accepted
- **Date:** 2026-11-06
- **Deciders:** @damnthonyy
- **Issue / PR:** #42

## Context

The network of a Kubernetes cluster is flat: every pod can open a connection
to every other pod, in any namespace. A preview runs the code of a pull
request, which may come from anyone allowed to open one. Without a rule, it
can call another preview, the databases of the real applications of the
cluster, the API server, or the metadata service of the cloud
(`169.254.169.254`), which hands out the credentials of the node on many
providers.

A NetworkPolicy selects pods and lists the traffic allowed to them (ingress)
and from them (egress); once a pod is selected in a direction, everything else
in that direction is denied. The network plugin of the cluster (the CNI)
enforces it, not the API server: without a CNI that supports NetworkPolicies,
a policy is accepted and ignored.

Two questions follow: who may call a preview, and what may a preview call.

## Decision

Each preview namespace gets one NetworkPolicy, `preview`, that selects every
pod in it and denies all traffic in both directions, except:

- **In:** the port of the application (`http`), from the pods of the preview,
  and from every namespace that is not a preview: namespaces without the label
  `app.kubernetes.io/managed-by=vesper`. The ingress controller is in one of
  them, wherever it is installed, so no setting is needed. Previews cannot
  call each other.
- **Out:** the pods of the preview; DNS (port 53, to any address, because the
  DNS server of the cluster has a private one); and the public internet: every
  address except the private ranges (`10.0.0.0/8`, `172.16.0.0/12`,
  `192.168.0.0/16`, `fc00::/7`), the link-local ranges (`169.254.0.0/16`,
  `fe80::/10`) and the shared address space (`100.64.0.0/10`). The pods, the
  Services and the nodes of a cluster live in those ranges. When the nodes or
  the API server have public addresses, those are denied too: the operator
  reads them from the nodes and from the EndpointSlices of the `kubernetes`
  Service, and keeps the policy of every preview up to date.

The operator creates the policy before the Deployment, so no pod of a preview
runs without it, and restores it when it is changed by hand.

## Alternatives considered

**Restrict ingress only.** Previews could not call each other, since the
target denies it, and applications calling internal services would keep
working. But the code of a pull request could still reach everything else in
the cluster and the metadata service of the cloud. Vesper exists to run that
code safely, so egress is restricted too.

**Allow only the namespace of the ingress controller in, set by a flag.**
Tighter, but every installation would have to configure it, and a wrong value
makes every preview unreachable. Namespaces that are not previews are trusted
by the cluster already; the goal is to keep previews apart.

**Deny all egress except DNS.** Safest, but most applications call external
APIs (payments, maps, authentication), and their previews would fail.

## Consequences

- A preview cannot call a service inside the cluster, such as a shared
  database. If teams need it, an operator flag to allow chosen destinations
  is a later change, and this ADR to revisit.
- The isolation depends on the CNI. kindnet (the CNI of recent kind
  releases), k3s, Calico and Cilium enforce NetworkPolicies; flannel alone
  does not. The docs say so, and must never promise hard multi-tenant
  isolation: a namespace stays a soft boundary. Since #167, CI checks the
  isolation with kindnet, Calico and Cilium; Cilium matches the nodes by
  identity and not by `ipBlock`, and the policy holds there too. A
  `CiliumNetworkPolicy` with `toEntities` would say the same without the
  list of addresses, at the price of depending on Cilium: an opt-in mode to
  consider later.
- Nodes with public addresses (a server with k3s, for example) were first
  missed: a preview could reach the API server and the kubelet through them.
  Denying the addresses of the cluster fixed it; the price is that a preview
  cannot call a public URL served by the same cluster, and that the operator
  reads nodes and EndpointSlices.
- An ingress controller on the host network (`hostNetwork`) connects with the
  address of the node, not from a namespace; whether the policy lets it in
  depends on the CNI.
- How fast a CNI applies the policy to a new pod differs. kindnet does not
  filter the first packets of a pod that connects as soon as it starts; the
  e2e test waits before probing.

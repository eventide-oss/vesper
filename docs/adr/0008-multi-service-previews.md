# ADR-0008: Describe a preview as a list of components that Vesper runs itself

- **Status:** Accepted
- **Date:** 2026-11-23
- **Deciders:** @damnthonyy
- **Issue / PR:** #162, #218

## Context

A preview runs one application from one image: one Deployment, one Service
and one Ingress, all named `app`, in a namespace of its own (ADR-0002). Most
applications need more to be tested end to end: an API behind the frontend, a
database with test data, sometimes a cache or a worker. Today a pull request
that changes the API and the frontend cannot be tried in one preview, and a
preview has no database unless it points at a shared one, which ADR-0005
forbids on purpose: a preview cannot reach anything private in the cluster.

The constraints:

- **The code of a preview is not reviewed.** It may come from a fork
  (#131). Whatever describes the preview must not let that code leave the
  namespace: no cluster-wide object, no host access, no new public endpoint.
  This is what Vesper promises, and what it must keep promising with several
  services.
- **Vesper never builds code** (ADR-0007). The CI of a project builds and
  pushes the images; Vesper runs them.
- **The namespace enforces the `restricted` Pod Security Standard.** Every
  container runs as a non-root user, with no privilege escalation and no
  capability. Many database images start as root by default.
- **Previews are short-lived and cheap.** They live hours or days, and come
  and go many times a day.
- **The API is `v1alpha1`**, used by v0.1 and v0.2 installs, with a single
  `image` per preview. Existing objects must keep working.

Teams who run previews with Argo CD or Flux render a Helm chart or a
kustomize directory per pull request. One of them, from the launch thread,
asked how to give each preview a database of its own.

## Decision

We add **`spec.components`**, a list of the services of a preview, to
`PreviewEnvironment` and to the defaults of a `PreviewSource`. Vesper renders
each component into objects it controls itself, with the same guarantees as
today. The single-image fields stay, as the shorthand for one component.

```yaml
spec:
  ttl: 48h
  components:
    - name: web
      image: ghcr.io/acme/web:pr-42-1a2b3c4
      port: 3000
      expose: true
      env:
        - name: API_URL
          value: http://api:8080
    - name: api
      image: ghcr.io/acme/api:pr-42-1a2b3c4
      port: 8080
      env:
        - name: DATABASE_HOST
          value: postgres
        - name: DATABASE_PASSWORD
          valueFrom:
            generatedSecret: postgres-password
    - name: postgres
      image: ghcr.io/acme/postgres-seed:pr-42-1a2b3c4
      port: 5432
      runAsUser: 999
      volumes:
        - path: /var/lib/postgresql/data
          sizeLimit: 1Gi
      env:
        - name: POSTGRES_PASSWORD
          valueFrom:
            generatedSecret: postgres-password
```

What each part means:

- **A component** is a name, an image, a port, a number of replicas and
  environment variables. Vesper makes one Deployment and one Service per
  component, both named after it. At most 10 components per preview.
- **Discovery**: a component reaches another by the name of its Service in
  the namespace of the preview, `http://api:8080` or `postgres:5432`. No
  configuration and no service mesh.
- **Exactly one component has `expose: true`**: the Ingress and the public
  URL go to it, and only it can be reached from outside the preview.
- **The single-image fields** (`image`, `port`, `replicas`, `env`) still work
  and mean one exposed component named `app`, so the objects of existing
  previews keep their names. A spec gives either them or `components`, never
  both: a CEL rule of the CRD rejects the mix.
- **Data**: a database is a component like the others. Its files live in an
  `emptyDir` volume (`volumes`), a directory that Kubernetes creates empty
  with the pod and deletes with it, with a size limit. **Seed data comes in
  the image**: the CI builds an image `FROM postgres` with its SQL files in
  `/docker-entrypoint-initdb.d`, per commit if the pull request changes them,
  as it already builds the application. Vesper adds no seeding mechanism.
- **Non-root images**: `runAsUser` sets the user of a component that starts
  as root by default, such as the official `postgres` image (user 999). It
  must not be 0; the namespace still enforces `restricted`.
- **Secrets**: `generatedSecrets` names values the operator generates once
  per preview, 32 random characters each, and keeps in a Secret of the
  preview namespace. Components read them with `valueFrom.generatedSecret`.
  No value is ever written in the spec, and the value dies with the preview.
- **Isolation** (amends ADR-0005): inside a preview, every pod can reach
  every port of every other pod. From namespaces that are not previews, only
  the pods of the exposed component can be reached, on its port. Egress does
  not change. The ResourceQuota and the LimitRange cover the whole preview,
  whatever the number of components.
- **Readiness**: the preview is `Ready` when the Deployment of every
  component is available. `status.components` lists each component with its
  state and, when it is not ready, the reason (image not found, crash loop,
  rollout stuck: #191). The comment on the pull request shows the same list.
  There is no start order: a component retries its connections, as on any
  Kubernetes cluster.
- **Pull requests**: in a `PreviewSource`, each component has its own image
  template. A template with `{{sha}}` follows the commits of the pull
  request; a template without it is a fixed image, pinned with a tag other
  than `latest`. At least one component must contain `{{sha}}`. The build
  workflow (`buildWorkflow`) pushes every image of a commit before it
  succeeds, and the preview switches all components to the new commit
  together.

## Alternatives considered

**Render a Helm chart or a kustomize directory per preview**, as Argo CD and
Flux do. Teams that already have a chart would reuse it, and Helm is what the
Kubernetes community knows. But a chart can create any object: a
`ClusterRole`, a pod with `hostPath` or `privileged`, a `LoadBalancer`
Service that costs money and opens a public address, a `CronJob` that
outlives the preview. Vesper would have to give its own service account the
rights to create all of them, and the chart of a pull request is code nobody
reviewed yet: a pull request, from a fork for example, could change its
`templates/` to escape the preview. Vesper would lose the one thing that
sets it apart, and teams that want Helm per pull request are already well
served by Argo CD and Flux.

It stays possible later, as an **opt-in mode with a warning**, if teams ask
for it, with several layers so that no single one has to hold:

- **The source**: the chart is read from the default branch, which was
  reviewed, or from a versioned chart in an OCI registry, optionally signed;
  the pull request only changes the image tags.
- **The rendering**: Vesper renders the chart itself, without access to the
  cluster, ignores Helm hooks and disables `lookup`, which would let a
  template read objects of the cluster such as Secrets.
- **A closed list of kinds**: Vesper accepts Deployment, StatefulSet,
  Service, ConfigMap, Secret, Job... and rejects the rest: every
  cluster-scoped object, `Role` and `RoleBinding`, `LoadBalancer` and
  `NodePort` Services, and Ingresses, since Vesper makes its own. Every
  object is moved to the namespace of the preview.
- **Impersonation**: Vesper applies the objects as a ServiceAccount of the
  preview, bound to a `Role` limited to its namespace, and not with its own
  rights. If the filter misses an object, the API server still refuses it.
- **Admission**: the namespace keeps the `restricted` profile, the quota and
  the NetworkPolicy, and a `ValidatingAdmissionPolicy` (CEL rules checked by
  the API server) forbids what a preview namespace must never hold, such as a
  `LoadBalancer` Service, whoever creates it.
- **People and visibility**: forks still need the `preview` label, a
  `CODEOWNERS` entry can require a review of the chart, and the status and the
  comment on the pull request list the objects created and those rejected,
  and say that the mode trusts the chart.

That needs an ADR of its own.

**A separate `PreviewTemplate` resource**, shared by the previews of a
repository and referenced by name. It would keep long stacks out of each
`PreviewEnvironment`. But the defaults of a `PreviewSource` already play that
role for pull requests, and a second resource adds a lookup, a lifecycle and
RBAC for little gain today. If people who create previews by hand ask for
it, it can be added without changing `components`.

**A database operator, such as CloudNativePG**, or a `StatefulSet` with a
`PersistentVolumeClaim`. Data would survive a restart of the pod, and
CloudNativePG brings backups and seeding from a snapshot. But it makes an
operator, its CRDs and its rights a requirement of Vesper, and a volume per
preview costs money, depends on the storage of the cluster and must be
cleaned up. For an environment that lives hours, an `emptyDir` is enough.
A persistent volume per component fits the cost milestone (v0.4), with the
sleep and wake of previews (#196).

**Seed data from a ConfigMap** that Vesper copies into the preview. No extra
image to build. But a ConfigMap holds at most 1 MiB, the operator would need
to read ConfigMaps in other namespaces, and it adds a second way to do what
an image already does.

**No generated secrets**: a fixed password in `env`, or a database that
trusts every connection, since only the pods of the preview can reach it.
The operator would need no right on Secrets. But passwords would end up in
specs, in Git and in the configuration copied to staging; a generated value
teaches the right habit and costs one Secret per preview.

## Consequences

- One pull request can be tried end to end, with its own database, and
  nothing it runs can reach another preview or the cluster.
- Every existing `PreviewEnvironment` and `PreviewSource` keeps working
  unchanged, with the same object names.
- The operator gets `get`, `create`, `update` and `delete` on Secrets, in
  every namespace, because a ClusterRole cannot be limited to the namespaces
  it labels. It never lists or watches them, so no Secret of the cluster
  enters its cache, and it only ever names Secrets in the namespaces of its
  previews. `docs/security.md` must say so. This is the largest new right
  since v0.1.
- Data does not survive a restart of a pod: the database starts again from
  its seed. The docs must say so.
- Images that need to start as root, or to write outside their volumes,
  cannot run. `runAsUser` and `volumes` cover the common databases
  (PostgreSQL, MySQL, Redis); others need an image built for non-root
  users. The e2e tests must run PostgreSQL to keep that promise.
- A preview with several components uses more of the quota (10 pods, 4 CPUs
  and 4 GiB by default). Administrators may need to raise it.
- No start order and no health check beyond the Deployment: a component that
  starts before the database must retry. A readiness probe per component is a
  later change if teams ask.
- `vesperctl create` keeps its flags for one image; a stack is created from
  a file, or through a `PreviewSource`.
- To revisit if teams ask for Helm, for persistent data, or for a component
  shared by several previews (#210).

Follow-up issues, in the milestone of v0.3:

1. API: `components`, its CEL rules and the single-image shorthand.
2. Controller: a Deployment and a Service per component, and the removal of
   the objects of a component that leaves the spec.
3. NetworkPolicy: only the exposed component reachable from outside, every
   port inside the preview.
4. `runAsUser` and `volumes` (`emptyDir` with a size limit).
5. `generatedSecrets`, and the new right on Secrets.
6. `status.components` and the comment on the pull request.
7. `PreviewSource`: an image template per component.
8. `vesperctl`: create a preview from a file, and show its components.
9. Docs and an example in vesper-demo with a frontend, an API and PostgreSQL,
   and an e2e test that runs it.

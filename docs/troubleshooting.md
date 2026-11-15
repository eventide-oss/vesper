# Troubleshooting

A preview that does not come up says why in its status: the reason of its
conditions. This page reads that reason, gives the commands that show the
cause, and the usual fix. The examples use a preview named `demo` in the
current namespace; replace it with yours.

## Start with the status

```sh
vesperctl get demo
kubectl describe pe demo
```

`vesperctl get` prints the phase, the URL, the expiry, the conditions and, since
#151, the latest events of the preview, so it is enough in most cases.
`kubectl describe` adds the full list of events and the rest of the object,
which is usually where a failure explains itself.

A preview carries up to three conditions:

| Type | True when |
| --- | --- |
| `Ready` | the application of the preview runs and is reachable |
| `Available` | every replica runs the requested image, behind its Service |
| `Expired` | the ttl has passed |

When `Ready` is False, its reason says what blocks. The reasons are the
constants of
[`previewenvironment_types.go`](../api/v1alpha1/previewenvironment_types.go);
the tables below cover all of them.

Most commands need the namespace the operator created for the preview:

```sh
NS=$(kubectl get pe demo -o jsonpath='{.status.namespace}')
```

It is empty until the namespace exists, so a preview that never got past
`Pending` has nothing to look at there yet.

## The phase

The phase summarizes the conditions in one word.

| Phase | Meaning | What to do |
| --- | --- | --- |
| `Pending` | the operator is setting the preview up | wait; if it lasts more than a couple of minutes, see [`Provisioning`](#pending-provisioning) |
| `Ready` | the application runs and is reachable | nothing, unless `Available` or `Expired` says otherwise |
| `Failed` | a problem blocks the preview; the reason of `Ready` says which | the section for that reason |
| `Expired` | the ttl has passed and the preview is being deleted | create it again, or [extend](cheatsheet.md#extend) the next one |

## The reasons

### Pending `Provisioning`

The operator is creating the namespace, the Deployment, the Service, the quota
and the NetworkPolicy, or the pods are starting. The message gives the number
of replicas available.

```sh
kubectl get pods -n "$NS"
kubectl events -n "$NS"
```

Wait: a first pull of a large image takes a while. If it lasts, the pods are
usually the cause, so read them as for [`RolloutStuck`](#failed-rolloutstuck).
An image that cannot be pulled and an application that keeps crashing are
reported at once, as [`ImagePullFailed`](#failed-imagepullfailed) and
[`CrashLooping`](#failed-crashlooping).

### Ready `DeploymentAvailable`

Nothing is wrong: every replica runs the requested image, behind its Service.
If the preview is still not reachable, the problem is in front of the pods, not
in them.

```sh
vesperctl get demo            # the URL line, empty without --preview-domain
kubectl get ingress -n "$NS"
kubectl port-forward -n "$NS" svc/app 8080:80
```

### Failed `PodsRejected`

The API server refuses the pods of the preview, most often because the quota
of its namespace is full. The message carries the error of the API server.

```sh
kubectl describe pe demo       # the message of the Ready condition
kubectl get resourcequota -n "$NS" -o yaml
kubectl get events -n "$NS" --field-selector reason=FailedCreate
```

The fix is to need less room or to have more:

- lower `--replicas` on the preview, since each replica is one more pod;
- raise the quota of the operator (`--quota-pods`, `--quota-cpu`,
  `--quota-memory`) and restart it;
- delete previews that are done with, which frees their namespace and its
  quota.

The preview goes back to `Ready` by itself once the pods fit.

### Failed `ImagePullFailed`

A pod of the preview cannot pull its image. The operator reads the pods, so it
says so within seconds, with the error of the kubelet in the message: `not
found` is a tag that does not exist, `unauthorized` or `denied` a private
image, a connection error a registry the cluster cannot reach.

```sh
kubectl describe pe demo       # the message of the Ready condition
kubectl describe pod -n "$NS" -l app.kubernetes.io/name=preview-app
```

Check the name and the tag with `docker pull`. With the GitHub integration,
the preview starts on its own once the image exists and can be pulled: the
kubelet keeps trying, and the operator follows.

### Failed `CrashLooping`

The application starts and exits again and again; Kubernetes waits longer
before each restart. The message gives the last exit code and the command to
read the logs of the run that failed.

```sh
kubectl logs -n "$NS" deploy/app --previous
```

The port, an environment variable or the command of the image is usually
wrong. Exit code 137 with `OOMKilled` means the application needs more memory
than the limit of the operator (`--default-memory-limit`).

### Failed `RolloutStuck`

The Deployment made no progress for its progress deadline, 10 minutes, for a
cause the operator does not report as `ImagePullFailed` or `CrashLooping`. It
is in the pods, and `kubectl get pods` names it.

```sh
kubectl get pods -n "$NS"
kubectl describe pod -n "$NS" -l app.kubernetes.io/name=preview-app
kubectl logs -n "$NS" deploy/app
```

| What the pod says | Cause | Fix |
| --- | --- | --- |
| `ImagePullBackOff`, `ErrImagePull` | the image or its tag does not exist, or is not reachable from the cluster | check the name and the tag with `docker pull`, and that the tag is not `latest`, which Vesper refuses |
| `CrashLoopBackOff` | the application starts and exits | `kubectl logs` shows why; the port, an environment variable or the command of the image is usually wrong |
| `CreateContainerConfigError` | the image runs as root | previews run under the `restricted` Pod Security profile, so use an image that runs as a non-root user, such as `nginxinc/nginx-unprivileged` |
| `Pending`, `Insufficient cpu` | the cluster has no room for the pod | free nodes or lower the requests of the operator (`--default-cpu-request`, `--default-memory-request`) |

Correcting the preview (its image, its port, its replicas) starts a new
rollout; there is nothing to restart by hand.

### Failed `NamespaceConflict`

The namespace the preview needs exists, and Vesper did not create it for this
preview. Vesper leaves it untouched rather than adopt or delete it, so the
preview never gets an application.

```sh
kubectl get pe demo -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
kubectl describe pe demo       # the same message, with the rest of the condition
```

If the namespace is a leftover of an earlier preview that you no longer need,
delete it: `kubectl delete namespace <name>`. If it holds something of yours,
keep it and create the preview under a different name, which gets a namespace
of its own.

### Failed `ReconcileError`

The operator failed to set up the preview. The message carries the error, and
the operator retries on its own, so the preview recovers when the cause is
gone.

```sh
kubectl describe pe demo       # the message of the Ready condition
kubectl logs -n vesper-system deploy/vesper-controller-manager
```

The usual cause is an object that Vesper needs and that already exists without
being Vesper's, for example a Service or a Deployment of the same name in the
preview namespace. Delete or rename the foreign object, and the next retry
succeeds. A failed write to the API server (admission webhook down, API server
restarting) shows up here too, and clears by itself.

### Expired `TTLPassed`

The ttl has passed: the preview is being deleted, with its namespace and
everything in it. This is the normal end of a preview, not a failure.

```sh
kubectl get pe demo -o jsonpath='{.metadata.deletionTimestamp}'
vesperctl list
```

Create the preview again for another ttl, or extend the next one before it
expires: `vesperctl extend demo --by 2h`. A preview lives 168h at most,
extensions included.

### Ready `Expired`

The ttl has passed and the deletion is in progress. Same commands as
`TTLPassed`. If it stays in this state, the namespace is not going away: see
[a deleted preview that stays](#a-deleted-preview-that-stays).

### Expired `TTLNotReached`

Nothing is wrong. `Expired` is False and its message gives the expiry. Extend
the preview if the time left is too short:

```sh
vesperctl extend demo --by 24h
```

## When the status says nothing

### Nothing happens at all

A preview that stays `Pending`, or a namespace that never appears, usually
means the operator is not running.

```sh
kubectl -n vesper-system rollout status deploy/vesper-controller-manager
kubectl -n vesper-system get pods
kubectl logs -n vesper-system deploy/vesper-controller-manager
```

Start it again: `make run` on a local cluster, or the install bundle of the
release ([quickstart](quickstart.md#2-install-the-operator)).

### The preview API is missing

When even `kubectl get pe` fails with a message about a resource type that the
server does not have, the CRD is not installed in the cluster.

```sh
kubectl get crd previewenvironments.vesper.eventide.dev
```

Install it with `make install` on a local cluster, or the install bundle of the
release.

### A deleted preview that stays

`vesperctl delete demo` returns and the preview stays, with a deletion
timestamp: the operator deletes the namespace and then removes the finalizer
`vesper.eventide.dev/cleanup`, and nobody did either.

```sh
kubectl get pe demo -o jsonpath='{.metadata.deletionTimestamp}'
kubectl get namespace "$NS"
```

Start the operator and the deletion completes. To give up the cleanup and
finish it yourself, remove the finalizer, then delete the namespace by its
labels:

```sh
kubectl patch pe demo --type=merge -p '{"metadata":{"finalizers":null}}'
kubectl delete namespace -l vesper.eventide.dev/preview-name=demo,vesper.eventide.dev/preview-namespace=<namespace of the preview>
```

### The preview has no address

`status.url` stays empty when the operator runs without `--preview-domain`: no
Ingress is created, by design.

```sh
vesperctl get demo             # the URL line, empty
kubectl get ingress -A | grep demo
```

Forward a local port instead, which needs no domain and no ingress controller:

```sh
kubectl port-forward -n "$NS" svc/app 8080:80
```

For a public address, start the operator again with `--preview-domain`, plus a
wildcard DNS record for it ([try Vesper on a server](try-on-a-server.md)).
Each reconciliation creates the Ingress of a preview that lacks one, so the
running previews get theirs on their next change.

### The preview is reachable but answers wrongly

The application runs, the address works, and the page is wrong: the problem is
in the application, not in Vesper.

```sh
kubectl logs -n "$NS" deploy/app -f
kubectl exec -n "$NS" deploy/app -- env
kubectl describe pe demo       # the port and the environment of the preview
```

Check that the port of the preview is the port the application listens on,
that its environment variables are the ones it expects, and that the image is
the build you meant to run.

## Related

- [Cheat sheet](cheatsheet.md): the everyday commands, including a shorter
  version of the table above.
- [vesperctl reference](cli.md): what each command prints, and its exit codes.
- [Development](development.md): the local kind cluster, the flags of the
  operator, and the finalizer in detail.
- [Security model](security.md): why previews run unprivileged, and the quota.

# Cheat sheet

The everyday commands, by task. `vesperctl` creates and reads previews;
`kubectl` does the rest until `vesperctl` covers it. Replace `demo` with the
name of your preview. For every option of `vesperctl`, see the
[vesperctl reference](cli.md).

## Words

| Word | Meaning |
| --- | --- |
| preview | a `PreviewEnvironment`: the object you create. Short name for kubectl: `pe` |
| preview namespace | the namespace Vesper creates for each preview, such as `vesper-default-demo-02c8c86b`. Your application runs there |
| operator | the program that watches previews and creates what they need. Locally, `make run` |
| `-n <namespace>` | the namespace to work in, for both tools. Without it: the namespace of your context, usually `default` |
| `-A` | every namespace |

## Set up (local cluster)

```sh
make kind-up    # create the local cluster vesper-dev (once)
make install    # install or update the PreviewEnvironment API in the cluster
make build      # build bin/vesperctl and bin/manager
make run        # run the operator; it keeps this terminal, stop it with Ctrl-C
```

Run `make install` and `make build` again after each `git pull`. Options of
the operator, for example a public domain or a smaller quota, go after
`go run ./cmd`:

```sh
go run ./cmd --preview-domain=preview.localtest.me --quota-pods=1
```

## Create

```sh
./bin/vesperctl create demo --image nginxinc/nginx-unprivileged:1.27 --port 8080
```

- `demo`: the name of the preview.
- `--image`: the image to run, with a tag other than `latest`. It must run
  as a non-root user (`nginx` does not, `nginxinc/nginx-unprivileged` does).
- `--port`: the port the application listens on inside its container. 8080 by
  default.

Optional: `--ttl 2h` (lifetime, 24h by default, 168h at most), `--replicas 2`,
`--env KEY=VALUE` (repeat it), `--dry-run` (print the YAML, create nothing).

## Follow

```sh
./bin/vesperctl list          # the previews of the namespace; -A for all
./bin/vesperctl get demo      # one preview: phase, expiry, conditions
kubectl get pe -w             # watch the phases change; Ctrl-C to stop
kubectl describe pe demo      # conditions, and at the bottom the events
```

## Find the preview namespace

```sh
kubectl get pe demo -o wide
```

`-o wide` adds columns, among them `INTERNAL URL`:
`http://app.<preview namespace>.svc.cluster.local`. The part between `app.`
and `.svc` is the preview namespace. (#86 will show it in `vesperctl get`.)

To keep it in a shell variable, as below: `NS=vesper-default-demo-02c8c86b`.

## Look inside

```sh
kubectl get all -n $NS        # the Deployment, its pods and the Service
kubectl logs -n $NS deploy/app -f   # the logs of the application; -f follows them
kubectl events -n $NS         # what happened in the namespace, oldest first
kubectl get limitrange,resourcequota,networkpolicy -n $NS   # the limits and the isolation
```

## Open the application

```sh
kubectl port-forward -n $NS svc/app 8080:80
```

Then open <http://localhost:8080>. `8080:80` means: port 8080 on your machine
goes to port 80 of the Service `app`. Ctrl-C closes it. With a preview domain,
the `URL` column of `vesperctl list` gives a public address instead.

## Extend

```sh
./bin/vesperctl extend demo --by 2h
```

- `--by 2h`: how much longer the preview lives. It raises the ttl: a preview
  of 24h becomes 26h, counted from its creation.
- A preview lives 168h (7 days) at most, extensions included. Beyond that,
  vesperctl refuses and says by how much it can still be extended.

## Delete

```sh
kubectl delete pe demo
```

The operator deletes the preview namespace and everything in it, then the
preview goes; it takes a few seconds. `vesperctl delete` is #48.

## When something is wrong

`vesperctl get demo` gives the phase and the reason of the `Ready`
condition:

| Phase, reason | What it means | What to do |
| --- | --- | --- |
| `Pending`, `Provisioning` | the pods are starting | wait; if it lasts, `kubectl get pods -n $NS` |
| `Failed`, `PodsRejected` | the API server refuses the pods, often because the quota is full | read the message; lower `--replicas`, or start the operator with a larger quota |
| `Failed`, `RolloutStuck` | no progress for 10 minutes | `kubectl get pods -n $NS`: `ImagePullBackOff` is a wrong image, `CrashLoopBackOff` an application that crashes (`kubectl logs`), `CreateContainerConfigError` an image that runs as root |
| `Failed`, `NamespaceConflict` | the preview namespace exists and is not Vesper's | delete that namespace, or choose another name |
| `Failed`, `ReconcileError` | the operator failed | read the message, and the logs of the operator |
| `Expired` | the ttl has passed | the preview is being deleted; create it again |

Nothing happens at all? Check that the operator runs (`make run`).

## Clean up

```sh
make kind-down   # delete the local cluster and everything in it
```

# vesperctl reference

`vesperctl` reads and creates the preview environments that Vesper manages
in a Kubernetes cluster. It uses your kubeconfig, like kubectl. Reading needs
`get` and `list` on `previewenvironments.vesper.eventide.dev`, creating
needs `create`.

Build it with `make build`; the binary is `bin/vesperctl`.

## Global flags

Every command accepts them, before or after the command name.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--kubeconfig <path>` | `$KUBECONFIG`, then `~/.kube/config` | kubeconfig file to read |
| `--context <name>` | the current context | kubeconfig context to use |
| `-n`, `--namespace <name>` | the namespace of the context, else `default` | namespace to work in |

## Commands

### `vesperctl list`

List the preview environments of the namespace, sorted by namespace then
name.

| Flag | Meaning |
| --- | --- |
| `-A`, `--all-namespaces` | list every namespace (overrides `--namespace`) |
| `-o`, `--output <format>` | `table` (default), `json` or `yaml` |

```text
$ vesperctl list -n team-a
NAMESPACE   NAME    PHASE    AGE
team-a      pr-12   <none>   5m
team-a      pr-42   Ready    90m
```

With no preview environment, the table output prints a message on stderr
and nothing on stdout. JSON and YAML always print a document:
`{"items": []}`.

### `vesperctl get <name>`

Show one preview environment: status, expiry, a summary of its spec and its
conditions.

| Flag | Meaning |
| --- | --- |
| `-o`, `--output <format>` | `table` (default), `json` or `yaml` |

```text
$ vesperctl get pr-42 -n team-a
Name:        pr-42
Namespace:   team-a
Runs in:     vesper-team-a-pr-42-3f9c2a1b
Created:     2026-11-05T10:00:00Z (120m ago)
Phase:       Ready
URL:         https://pr-42.preview.example.com
Expires:     2026-11-06T10:00:00Z (in 22h)
Spec:
  Image:     ghcr.io/eventide/app:pr-42
  Port:      8080
  Replicas:  1
  TTL:       24h
  Env:       2 variables
Conditions:
  TYPE          STATUS   REASON            AGE   MESSAGE
  Available     True     DeploymentReady   90m   1/1 replicas available
  Progressing   False    Complete          90m   <none>
```

`<none>` marks a value that is not set yet. Environment variables may hold
secrets: only their number is shown, in every output format.

`Runs in` is the namespace the operator created for the preview, where its
application runs; it is set once the namespace exists. Its pods, logs and
events are there:

```sh
NS=$(kubectl get pe pr-42 -n team-a -o jsonpath='{.status.namespace}')
kubectl get pods -n "$NS"           # the pods of the preview
kubectl logs -n "$NS" deploy/app    # the logs of its application; -f follows them
kubectl events -n "$NS"             # what happened there, oldest first
```

### `vesperctl create <name> --image <image>`

Create a preview environment in the namespace. The operator then runs its
image; follow it with `vesperctl get`.

| Flag | Meaning |
| --- | --- |
| `--image <image>` | container image to run, pinned with a tag other than `latest` or with a digest (required) |
| `--port <port>` | port the application listens on; default `8080` |
| `--replicas <n>` | number of pods; default `1` |
| `--ttl <duration>` | time to live from the creation, such as `2h` or `90m`; default `24h` |
| `--env KEY=VALUE` | environment variable of the application; repeat it for several |
| `--dry-run` | print the preview environment as YAML instead of creating it |

```text
$ vesperctl create pr-42 -n team-a --image ghcr.io/acme/app:pr-42 --ttl 48h
preview environment "pr-42" created in namespace "team-a"
Follow it with: vesperctl get pr-42 -n team-a
```

Flags that are not given are left out, and the API server applies its
defaults. vesperctl checks the form of the flags (a number, a duration,
`KEY=VALUE`); the API server checks the values, with the rules of the CRD
(port, replicas, ttl between 1m and 168h, image), and vesperctl prints its
answer:

```text
$ vesperctl create pr-42 --image ghcr.io/acme/app:latest --ttl 30s
vesperctl: the API server rejected the preview environment: spec.image: Invalid value: "ghcr.io/acme/app:latest": image must be pinned with a tag other than latest, or with a digest; spec.ttl: Invalid value: "30s": ttl must be a duration such as 24h or 90m, between 1m and 168h
```

`--dry-run` sends nothing to the cluster: the output can be saved and
applied later, for example with `kubectl apply -f`. The values are only
checked when it is applied.

### `vesperctl extend <name> --by <duration>`

Extend a preview environment: raise its ttl by `--by`, such as `2h` or
`90m`. The ttl counts from the creation of the preview, so the expiry moves by
the same amount; the operator writes it to `status.expiresAt`.

```text
$ vesperctl extend pr-42 -n team-a --by 2h
preview environment "pr-42" extended by 2h0m0s: ttl 26h0m0s, expires at 2026-11-08T12:00:00Z
```

A ttl cannot exceed 168h: a preview lives 7 days at most, extensions
included (ADR-0004). vesperctl checks it before sending anything, and says by
how much the preview can still be extended:

```text
$ vesperctl extend pr-42 --by 145h
vesperctl: cannot extend "pr-42" by 145h0m0s: its ttl would be 169h0m0s, over the maximum of 168h0m0s; it can be extended by 144h0m0s at most
```

A preview that is being deleted, after its expiry or a `vesperctl delete`,
cannot be extended: create it again. If someone else changes the preview at
the same time, vesperctl reads it again and extends the new ttl, so no
extension is lost.

### `vesperctl delete <name>`

Delete a preview environment. vesperctl first asks for a confirmation on
stderr and reads the answer on stdin: `y` or `yes` deletes, anything else,
an empty line included, does not and exits with `1`.

```text
$ vesperctl delete pr-42 -n team-a
Delete preview environment "pr-42" in namespace "team-a", with everything in its preview namespace? [y/N] y
preview environment "pr-42" is being deleted: the operator deletes its namespace, then the preview
```

| Flag | Meaning |
| --- | --- |
| `-y`, `--yes` | delete without asking for confirmation |

In a script, pass `--yes`: without it and with nothing to read on stdin,
vesperctl does not delete, and says so. vesperctl returns once the deletion
is requested: the operator deletes the preview namespace and everything in
it, then the preview, within a few seconds. A preview that is already being
deleted is left as it is, with exit code `0`. If the preview was deleted and
created again between the question and the answer, the new one is not
deleted.

### `vesperctl completion bash|zsh|fish`

Print a shell completion script. It completes commands, flags, the values of
`-o` and the shell names.

```sh
# bash (needs the bash-completion package)
echo 'source <(vesperctl completion bash)' >> ~/.bashrc

# zsh (the completion system must be loaded: `autoload -U compinit && compinit`,
# which frameworks such as Oh My Zsh already do)
echo 'source <(vesperctl completion zsh)' >> ~/.zshrc

# fish
vesperctl completion fish > ~/.config/fish/completions/vesperctl.fish
```

Then start a new shell.

### `vesperctl version`

Print the version of vesperctl, the Go version and the platform.

## Output formats

`-o json` and `-o yaml` print a summary of each preview environment, not the
raw Kubernetes object. The summary is a stable contract for scripts: fields
may be added in later versions, and existing fields keep their name and
meaning. Use `kubectl get previewenvironment <name> -o yaml` for the raw
object.

`get` prints one summary; `list` prints `{"items": [...]}`.

| Field | Type | Meaning |
| --- | --- | --- |
| `name`, `namespace` | string | always present |
| `runNamespace` | string | the namespace where the application of the preview runs; omitted until the operator creates it |
| `createdAt` | string | creation time, RFC 3339 in UTC |
| `phase` | string | `Pending`, `Ready`, `Failed` or `Expired`; omitted until the controller sets it |
| `url` | string | address of the preview; omitted until set |
| `expiresAt` | string | when the preview is deleted, RFC 3339 in UTC; omitted until set |
| `spec.image`, `spec.ttl` | string | as requested; omitted when unset |
| `spec.port`, `spec.replicas` | integer | as requested; omitted when unset |
| `spec.envCount` | integer | number of environment variables; names and values are never printed |
| `conditions[]` | array | `type` (the main one is `Ready`), `status`, `reason`, `message`, `lastTransitionTime`, in the order the controller set them; omitted when empty |

JSON fields keep this order. YAML keys are sorted alphabetically, as kubectl
does.

```sh
vesperctl list -A -o json | jq -r '.items[] | select(.phase == "Ready") | .url'
```

## Exit codes

| Code | Meaning | Examples |
| --- | --- | --- |
| `0` | success | the command did what was asked, including an empty `list` |
| `1` | the command was valid but failed | preview not found, deletion not confirmed, permission denied, cluster unreachable, no kubeconfig, Vesper not installed |
| `2` | the command line is wrong | unknown command or flag, missing or invalid name, unknown output format or shell, a flag that is not a number or a duration, a value the API server rejects, an extension over 168h |

A usage error (`2`) also prints the usage of the command, except when the API
server rejected a value or an extension goes over 168h.

## Error messages

Errors are printed on stderr, on one line starting with `vesperctl:`, and say
what to do next when vesperctl can tell:

| Situation | Message |
| --- | --- |
| no kubeconfig | `no kubeconfig found: set --kubeconfig or the KUBECONFIG environment variable` |
| Vesper not installed | `the PreviewEnvironment API (...) is not installed in this cluster: install Vesper, or check --context` |
| preview not found | `preview environment "pr-1" not found in namespace "team-a"` |
| preview already exists (`create`) | `preview environment "pr-1" already exists in namespace "team-a": delete it first, or choose another name` |
| namespace not found (`create`) | `namespace "team-a" not found: create it, or choose another with --namespace` |
| ttl over 168h (`extend`) | `cannot extend "pr-1" by 145h0m0s: its ttl would be 169h0m0s, over the maximum of 168h0m0s; it can be extended by 144h0m0s at most` |
| deletion not confirmed (`delete`) | `not deleted` |
| no answer on stdin (`delete`) | `no answer: not deleted; pass --yes to delete without confirmation` |
| preview being deleted (`extend`) | `preview environment "pr-1" is being deleted: it cannot be extended; create it again` |
| value rejected by the API server (`create`) | `the API server rejected the preview environment: <field>: <reason>; ...` |
| 403 from the API server | `you are not allowed to list preview environments in namespace "team-a": ask a cluster admin for access (RBAC)` |
| 401 from the API server | `the API server rejected your credentials: check your kubeconfig, or log in again` |
| no response (refused, timeout, TLS) | `cannot reach the API server at https://...:6443 (check --context and your network): <cause>` |

Requests time out after 30 seconds. A Go stack trace is never printed: if
vesperctl hits a bug, it prints `internal error: ... (this is a bug, please
report it at https://github.com/eventide-oss/vesper/issues)` and exits with
`1`.

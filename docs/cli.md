# vesperctl reference

`vesperctl` reads the preview environments that Vesper manages in a
Kubernetes cluster. It uses your kubeconfig, like kubectl, and only needs
read access to `previewenvironments.vesper.eventide.dev`.

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
| `1` | the command was valid but failed | preview not found, permission denied, cluster unreachable, no kubeconfig, Vesper not installed |
| `2` | the command line is wrong | unknown command or flag, missing or invalid name, unknown output format or shell |

A usage error (`2`) also prints the usage of the command.

## Error messages

Errors are printed on stderr, on one line starting with `vesperctl:`, and say
what to do next when vesperctl can tell:

| Situation | Message |
| --- | --- |
| no kubeconfig | `no kubeconfig found: set --kubeconfig or the KUBECONFIG environment variable` |
| Vesper not installed | `the PreviewEnvironment API (...) is not installed in this cluster: install Vesper, or check --context` |
| preview not found | `preview environment "pr-1" not found in namespace "team-a"` |
| 403 from the API server | `you are not allowed to list preview environments in namespace "team-a": ask a cluster admin for access (RBAC)` |
| 401 from the API server | `the API server rejected your credentials: check your kubeconfig, or log in again` |
| no response (refused, timeout, TLS) | `cannot reach the API server at https://...:6443 (check --context and your network): <cause>` |

Requests time out after 30 seconds. A Go stack trace is never printed: if
vesperctl hits a bug, it prints `internal error: ... (this is a bug, please
report it at https://github.com/eventide-oss/vesper/issues)` and exits with
`1`.

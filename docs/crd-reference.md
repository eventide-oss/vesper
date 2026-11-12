# API Reference

## Packages
- [vesper.eventide.dev/v1alpha1](#vespereventidedevv1alpha1)


## vesper.eventide.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the vesper v1alpha1 API group.

### Resource Types
- [PreviewEnvironment](#previewenvironment)
- [PreviewSource](#previewsource)



#### EnvVar



EnvVar is an environment variable set in the preview's container.



_Appears in:_
- [PreviewDefaults](#previewdefaults)
- [PreviewEnvironmentSpec](#previewenvironmentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | name of the variable, for example LOG_LEVEL. |  | MaxLength: 256 <br />MinLength: 1 <br />Pattern: `^[A-Za-z_][A-Za-z0-9_]*$` <br />Required: \{\} <br /> |
| `value` _string_ | value of the variable. Empty by default. |  | MaxLength: 4096 <br />Optional: \{\} <br /> |


#### ForkPolicy

_Underlying type:_ _string_

ForkPolicy chooses whether pull requests from forks get a preview.

_Validation:_
- Enum: [Deny RequireLabel]

_Appears in:_
- [PreviewSourceSpec](#previewsourcespec)

| Field | Description |
| --- | --- |
| `Deny` | ForkDeny gives no preview to pull requests from forks.<br /> |
| `RequireLabel` | ForkRequireLabel gives one once someone with write access to the<br />repository adds the label preview.<br /> |


#### Phase

_Underlying type:_ _string_

Phase summarizes the state of a preview in one word. The conditions hold
the details.

_Validation:_
- Enum: [Pending Ready Failed Expired]

_Appears in:_
- [PreviewEnvironmentStatus](#previewenvironmentstatus)

| Field | Description |
| --- | --- |
| `Pending` | PhasePending means the preview is being set up.<br /> |
| `Ready` | PhaseReady means the application of the preview is running.<br /> |
| `Failed` | PhaseFailed means a problem blocks the preview. The conditions say which.<br /> |
| `Expired` | PhaseExpired means the TTL has passed and the preview is being deleted.<br /> |


#### PreviewDefaults



PreviewDefaults are the settings of the previews of a source, with the
bounds of a PreviewEnvironment.



_Appears in:_
- [PreviewSourceSpec](#previewsourcespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `port` _integer_ | port is the port the application listens on, inside its container. | 8080 | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `replicas` _integer_ | replicas is the number of pods of each preview. | 1 | Maximum: 5 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `ttl` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#duration-v1-meta)_ | ttl is how long a preview lives after the last commit of its pull<br />request, from 1m to 168h. | 24h | Type: string <br />Optional: \{\} <br /> |
| `env` _[EnvVar](#envvar) array_ | env are environment variables given to each preview. Their values are<br />readable by anyone who can read this source: no secrets. |  | MaxItems: 64 <br />Optional: \{\} <br /> |


#### PreviewEnvironment



PreviewEnvironment is a temporary environment that runs one container
image, for example the build of a pull request, until its TTL expires.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `vesper.eventide.dev/v1alpha1` | | |
| `kind` _string_ | `PreviewEnvironment` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[PreviewEnvironmentSpec](#previewenvironmentspec)_ | spec defines the desired state of PreviewEnvironment |  | Required: \{\} <br /> |
| `status` _[PreviewEnvironmentStatus](#previewenvironmentstatus)_ | status defines the observed state of PreviewEnvironment |  | Optional: \{\} <br /> |


#### PreviewEnvironmentSpec



PreviewEnvironmentSpec is the preview a user asks for: which image to run,
how, and for how long.



_Appears in:_
- [PreviewEnvironment](#previewenvironment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `image` _string_ | image is the container image to run, pinned with a tag other than<br />"latest" or with a digest, for example ghcr.io/acme/app:pr-42 or<br />ghcr.io/acme/app@sha256:<64 hex characters>. A preview must show exactly<br />the code it was created for, and a moving tag would not. |  | MaxLength: 512 <br />MinLength: 1 <br />Required: \{\} <br /> |
| `port` _integer_ | port is the port the container listens on. Defaults to 8080. | 8080 | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `replicas` _integer_ | replicas is the number of pods to run. Defaults to 1, at most 5. | 1 | Maximum: 5 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `ttl` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#duration-v1-meta)_ | ttl is how long the preview lives after its creation, as a duration such<br />as "24h" or "90m", between 1m and 168h (7 days). Defaults to 24h. When it<br />has passed, the preview is deleted. Raise it to extend the preview: the<br />total lifetime stays within 168h. | 24h | Type: string <br />Optional: \{\} <br /> |
| `env` _[EnvVar](#envvar) array_ | env sets environment variables in the container. Names are unique.<br />Values are stored in plain text in the object: do not put secrets here. |  | MaxItems: 64 <br />Optional: \{\} <br /> |


#### PreviewEnvironmentStatus



PreviewEnvironmentStatus is the state of a preview as observed by the
operator. Only the operator writes it, through the status subresource.



_Appears in:_
- [PreviewEnvironment](#previewenvironment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[Phase](#phase)_ | phase summarizes the state of the preview: Pending, Ready, Failed or<br />Expired. |  | Enum: [Pending Ready Failed Expired] <br />Optional: \{\} <br /> |
| `namespace` _string_ | namespace is the namespace the operator created for the preview, where<br />its application runs, for example vesper-team-a-pr-42-3f9c2a1b. It is<br />set once the namespace exists; look there for the pods, logs and events<br />of the preview. |  | MaxLength: 63 <br />Optional: \{\} <br /> |
| `url` _string_ | url is the address where the preview can be reached. |  | MaxLength: 2048 <br />Optional: \{\} <br /> |
| `internalURL` _string_ | internalURL is the address of the preview inside the cluster: its<br />Service, for example http://app.vesper-team-a-pr-42-3f9c2a1b.svc.cluster.local.<br />It is not reachable from outside the cluster. |  | MaxLength: 2048 <br />Optional: \{\} <br /> |
| `expiresAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#time-v1-meta)_ | expiresAt is when the preview will be deleted: its creation time plus<br />its ttl. |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | observedGeneration is the metadata.generation of the spec this status<br />reflects. When it is lower than metadata.generation, the operator has<br />not processed the latest change yet. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#condition-v1-meta) array_ | conditions describe the state of the preview in detail: "Ready" sums<br />it up, "Available" says whether its application runs, and "Expired"<br />whether its ttl has passed. Each condition has a status (True, False or<br />Unknown), a machine-readable reason and a human-readable message. |  | Optional: \{\} <br /> |


#### PreviewSource



PreviewSource allows a GitHub repository to have previews of its pull
requests. It is cluster-scoped: an administrator of the cluster creates
it, not the teams, since it lets a repository run code on the cluster.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `vesper.eventide.dev/v1alpha1` | | |
| `kind` _string_ | `PreviewSource` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[PreviewSourceSpec](#previewsourcespec)_ | spec is the repository and the settings of its previews |  | Required: \{\} <br /> |


#### PreviewSourceSpec



PreviewSourceSpec allows a GitHub repository to have previews of its pull
requests, and says how to build them (ADR-0007).



_Appears in:_
- [PreviewSource](#previewsource)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `repository` _string_ | repository is the GitHub repository, as <owner>/<name>, for example<br />acme/app. GitHub compares it without case. |  | MaxLength: 140 <br />Pattern: `^[A-Za-z0-9]([A-Za-z0-9-]\{0,37\}[A-Za-z0-9])?/[A-Za-z0-9._-]\{1,100\}$` <br />Required: \{\} <br /> |
| `image` _string_ | image is the image of a pull request, as a template: \{\{number\}\} is<br />replaced by the number of the pull request, and \{\{sha\}\} by its head<br />commit, for example ghcr.io/acme/app:pr-\{\{number\}\}-\{\{sha\}\}. It must<br />contain \{\{sha\}\}, so that each commit has an image of its own, and give<br />a valid image reference pinned with a tag other than latest. |  | MaxLength: 512 <br />MinLength: 1 <br />Required: \{\} <br /> |
| `buildWorkflow` _string_ | buildWorkflow is the file name of the GitHub Actions workflow that<br />builds the image, for example preview.yml: a preview switches to the<br />image of a commit once a run of this workflow succeeds on it. Empty<br />when the image is pushed before the pull request event. |  | MaxLength: 100 <br />Pattern: `^[A-Za-z0-9._-]+[.]ya?ml$` <br />Optional: \{\} <br /> |
| `namespace` _string_ | namespace is where the PreviewEnvironment objects of this source are<br />created. Each of them still runs in a namespace of its own. |  | MaxLength: 63 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Required: \{\} <br /> |
| `trigger` _[Trigger](#trigger)_ | trigger chooses which pull requests get a preview: All, every pull<br />request, and the label no-preview stops one; Label, only the pull<br />requests with the label preview. | All | Enum: [All Label] <br />Optional: \{\} <br /> |
| `forkPullRequests` _[ForkPolicy](#forkpolicy)_ | forkPullRequests chooses whether pull requests from forks get a<br />preview: Deny, never; RequireLabel, once someone with write access to<br />the repository adds the label preview. Their code is not reviewed. | Deny | Enum: [Deny RequireLabel] <br />Optional: \{\} <br /> |
| `defaults` _[PreviewDefaults](#previewdefaults)_ | defaults are the settings of the previews of this source. | \{  \} | Optional: \{\} <br /> |


#### Trigger

_Underlying type:_ _string_

Trigger chooses which pull requests of a repository get a preview.

_Validation:_
- Enum: [All Label]

_Appears in:_
- [PreviewSourceSpec](#previewsourcespec)

| Field | Description |
| --- | --- |
| `All` | TriggerAll gives every pull request a preview; the label no-preview<br />stops one.<br /> |
| `Label` | TriggerLabel gives a preview only to the pull requests with the label<br />preview.<br /> |



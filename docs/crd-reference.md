# API Reference

## Packages
- [vesper.eventide.dev/v1alpha1](#vespereventidedevv1alpha1)


## vesper.eventide.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the vesper v1alpha1 API group.

### Resource Types
- [PreviewEnvironment](#previewenvironment)



#### EnvVar



EnvVar is an environment variable set in the preview's container.



_Appears in:_
- [PreviewEnvironmentSpec](#previewenvironmentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | name of the variable, for example LOG_LEVEL. |  | MaxLength: 256 <br />MinLength: 1 <br />Pattern: `^[A-Za-z_][A-Za-z0-9_]*$` <br />Required: \{\} <br /> |
| `value` _string_ | value of the variable. Empty by default. |  | MaxLength: 4096 <br />Optional: \{\} <br /> |


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



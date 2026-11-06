/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// PreviewEnvironmentSpec is the preview a user asks for: which image to run,
// how, and for how long.
type PreviewEnvironmentSpec struct {
	// image is the container image to run, pinned with a tag other than
	// "latest" or with a digest, for example ghcr.io/acme/app:pr-42 or
	// ghcr.io/acme/app@sha256:<64 hex characters>. A preview must show exactly
	// the code it was created for, and a moving tag would not.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self.matches('^([a-z0-9]([a-z0-9-]*[a-z0-9])?([.][a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]+)?/)?[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*(/[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$')",message="image must be a valid image reference in lowercase, such as ghcr.io/acme/app:pr-42"
	// +kubebuilder:validation:XValidation:rule="self.matches('@sha256:[a-f0-9]{64}$') || (self.matches(':[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$') && !self.endsWith(':latest'))",message="image must be pinned with a tag other than latest, or with a digest"
	Image string `json:"image"`

	// port is the port the container listens on. Defaults to 8080.
	// +optional
	// +kubebuilder:default=8080
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// replicas is the number of pods to run. Defaults to 1, at most 5.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Replicas int32 `json:"replicas,omitempty"`

	// ttl is how long the preview lives after its creation, as a duration such
	// as "24h" or "90m", between 1m and 168h (7 days). Defaults to 24h. When it
	// has passed, the preview is deleted. Raise it to extend the preview: the
	// total lifetime stays within 168h.
	// +optional
	// +kubebuilder:default="24h"
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:XValidation:rule="self.matches('^([0-9]+([.][0-9]+)?(h|m|s))+$') && duration(self) >= duration('1m') && duration(self) <= duration('168h')",message="ttl must be a duration such as 24h or 90m, between 1m and 168h"
	TTL *metav1.Duration `json:"ttl,omitempty"`

	// env sets environment variables in the container. Names are unique.
	// Values are stored in plain text in the object: do not put secrets here.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Env []EnvVar `json:"env,omitempty"`
}

// EnvVar is an environment variable set in the preview's container.
type EnvVar struct {
	// name of the variable, for example LOG_LEVEL.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// value of the variable. Empty by default.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Value string `json:"value,omitempty"`
}

// Phase summarizes the state of a preview in one word. The conditions hold
// the details.
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Expired
type Phase string

const (
	// PhasePending means the preview is being set up.
	PhasePending Phase = "Pending"
	// PhaseReady means the application of the preview is running.
	PhaseReady Phase = "Ready"
	// PhaseFailed means a problem blocks the preview. The conditions say which.
	PhaseFailed Phase = "Failed"
	// PhaseExpired means the TTL has passed and the preview is being deleted.
	PhaseExpired Phase = "Expired"
)

// Condition types of a preview.
const (
	// ConditionReady sums up the preview: True when its application is
	// running and reachable. When it is False, its reason says what blocks.
	ConditionReady = "Ready"
	// ConditionAvailable is True when every replica of the application runs
	// the requested image.
	ConditionAvailable = "Available"
	// ConditionExpired is True once the ttl of the preview has passed.
	ConditionExpired = "Expired"
)

// Reasons of the conditions of a preview, and of the events of the same name.
const (
	// ReasonProvisioning means the operator is setting up the preview, or
	// its application is starting (Ready, Available).
	ReasonProvisioning = "Provisioning"
	// ReasonDeploymentAvailable means every replica of the application runs
	// the requested image, behind its Service (Ready, Available).
	ReasonDeploymentAvailable = "DeploymentAvailable"
	// ReasonPodsRejected means the API server rejects the pods of the
	// preview, for example because its ResourceQuota is full. The message
	// gives the error (Ready, Available).
	ReasonPodsRejected = "PodsRejected"
	// ReasonRolloutStuck means the application made no progress for the
	// progress deadline of its Deployment, 10 minutes: often an image that
	// cannot be pulled, or a container that keeps crashing (Ready, Available).
	ReasonRolloutStuck = "RolloutStuck"
	// ReasonNamespaceConflict means the namespace the preview needs exists and
	// was not created by Vesper for this preview. Vesper leaves it untouched
	// (Ready, Available).
	ReasonNamespaceConflict = "NamespaceConflict"
	// ReasonReconcileError means the operator failed to set up the preview,
	// for example because an object it needs exists and is not Vesper's. The
	// message gives the error, and the operator retries (Ready).
	ReasonReconcileError = "ReconcileError"
	// ReasonExpired means the ttl of the preview has passed: it is being
	// deleted (Ready).
	ReasonExpired = "Expired"
	// ReasonTTLNotReached means the ttl has not passed. The message gives the
	// expiry (Expired is False).
	ReasonTTLNotReached = "TTLNotReached"
	// ReasonTTLPassed means the ttl has passed (Expired is True).
	ReasonTTLPassed = "TTLPassed"
)

// PreviewEnvironmentStatus is the state of a preview as observed by the
// operator. Only the operator writes it, through the status subresource.
type PreviewEnvironmentStatus struct {
	// phase summarizes the state of the preview: Pending, Ready, Failed or
	// Expired.
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// url is the address where the preview can be reached.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url,omitempty"`

	// internalURL is the address of the preview inside the cluster: its
	// Service, for example http://app.vesper-team-a-pr-42-3f9c2a1b.svc.cluster.local.
	// It is not reachable from outside the cluster.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	InternalURL string `json:"internalURL,omitempty"`

	// expiresAt is when the preview will be deleted: its creation time plus
	// its ttl.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// observedGeneration is the metadata.generation of the spec this status
	// reflects. When it is lower than metadata.generation, the operator has
	// not processed the latest change yet.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the state of the preview in detail: "Ready" sums
	// it up, "Available" says whether its application runs, and "Expired"
	// whether its ttl has passed. Each condition has a status (True, False or
	// Unknown), a machine-readable reason and a human-readable message.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pe
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63",message="name must be at most 63 characters: it is used as a label value on the objects of the preview"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Expires",type=string,JSONPath=`.status.expiresAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`,priority=1
// +kubebuilder:printcolumn:name="TTL",type=string,JSONPath=`.spec.ttl`,priority=1
// +kubebuilder:printcolumn:name="Internal URL",type=string,JSONPath=`.status.internalURL`,priority=1

// PreviewEnvironment is a temporary environment that runs one container
// image, for example the build of a pull request, until its TTL expires.
type PreviewEnvironment struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PreviewEnvironment
	// +required
	Spec PreviewEnvironmentSpec `json:"spec"`

	// status defines the observed state of PreviewEnvironment
	// +optional
	Status PreviewEnvironmentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PreviewEnvironmentList contains a list of PreviewEnvironment
type PreviewEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PreviewEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PreviewEnvironment{}, &PreviewEnvironmentList{})
		return nil
	})
}

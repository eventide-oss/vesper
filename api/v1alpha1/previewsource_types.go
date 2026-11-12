// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Trigger chooses which pull requests of a repository get a preview.
// +kubebuilder:validation:Enum=All;Label
type Trigger string

const (
	// TriggerAll gives every pull request a preview; the label no-preview
	// stops one.
	TriggerAll Trigger = "All"
	// TriggerLabel gives a preview only to the pull requests with the label
	// preview.
	TriggerLabel Trigger = "Label"
)

// ForkPolicy chooses whether pull requests from forks get a preview.
// +kubebuilder:validation:Enum=Deny;RequireLabel
type ForkPolicy string

const (
	// ForkDeny gives no preview to pull requests from forks.
	ForkDeny ForkPolicy = "Deny"
	// ForkRequireLabel gives one once someone with write access to the
	// repository adds the label preview.
	ForkRequireLabel ForkPolicy = "RequireLabel"
)

// PreviewSourceSpec allows a GitHub repository to have previews of its pull
// requests, and says how to build them (ADR-0007).
type PreviewSourceSpec struct {
	// repository is the GitHub repository, as <owner>/<name>, for example
	// acme/app. GitHub compares it without case.
	// +required
	// +kubebuilder:validation:MaxLength=140
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?/[A-Za-z0-9._-]{1,100}$`
	// +kubebuilder:validation:XValidation:rule="!self.endsWith('/.') && !self.endsWith('/..')",message="repository must be <owner>/<name>, and the name cannot be . or .."
	Repository string `json:"repository"`

	// image is the image of a pull request, as a template: {{number}} is
	// replaced by the number of the pull request, and {{sha}} by its head
	// commit, for example ghcr.io/acme/app:pr-{{number}}-{{sha}}. It must
	// contain {{sha}}, so that each commit has an image of its own, and give
	// a valid image reference pinned with a tag other than latest.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self.contains('{{sha}}')",message="image must contain {{sha}}, so that each commit has an image of its own"
	// +kubebuilder:validation:XValidation:rule="self.replace('{{number}}', '1').replace('{{sha}}', '0123456789abcdef0123456789abcdef01234567').matches('^([a-z0-9]([a-z0-9-]*[a-z0-9])?([.][a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]+)?/)?[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*(/[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*)*:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$')",message="image must give a valid image reference with a tag, in lowercase, such as ghcr.io/acme/app:pr-{{number}}-{{sha}}; {{number}} and {{sha}} are the only placeholders"
	// +kubebuilder:validation:XValidation:rule="!self.endsWith(':latest')",message="image must not use the tag latest"
	Image string `json:"image"`

	// buildWorkflow is the file name of the GitHub Actions workflow that
	// builds the image, for example preview.yml: a preview switches to the
	// image of a commit once a run of this workflow succeeds on it. Empty
	// when the image is pushed before the pull request event.
	// +optional
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._-]+[.]ya?ml$`
	BuildWorkflow string `json:"buildWorkflow,omitempty"`

	// namespace is where the PreviewEnvironment objects of this source are
	// created. Each of them still runs in a namespace of its own.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// trigger chooses which pull requests get a preview: All, every pull
	// request, and the label no-preview stops one; Label, only the pull
	// requests with the label preview.
	// +optional
	// +kubebuilder:default=All
	Trigger Trigger `json:"trigger,omitempty"`

	// forkPullRequests chooses whether pull requests from forks get a
	// preview: Deny, never; RequireLabel, once someone with write access to
	// the repository adds the label preview. Their code is not reviewed.
	// +optional
	// +kubebuilder:default=Deny
	ForkPullRequests ForkPolicy `json:"forkPullRequests,omitempty"`

	// defaults are the settings of the previews of this source.
	// +optional
	// +kubebuilder:default={}
	Defaults PreviewDefaults `json:"defaults,omitzero"`
}

// PreviewDefaults are the settings of the previews of a source, with the
// bounds of a PreviewEnvironment.
type PreviewDefaults struct {
	// port is the port the application listens on, inside its container.
	// +optional
	// +kubebuilder:default=8080
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// replicas is the number of pods of each preview.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Replicas int32 `json:"replicas,omitempty"`

	// ttl is how long a preview lives after the last commit of its pull
	// request, from 1m to 168h.
	// +optional
	// +kubebuilder:default="24h"
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:XValidation:rule="self.matches('^([0-9]+([.][0-9]+)?(h|m|s))+$') && duration(self) >= duration('1m') && duration(self) <= duration('168h')",message="ttl must be a duration such as 24h or 90m, between 1m and 168h"
	TTL *metav1.Duration `json:"ttl,omitempty"`

	// env are environment variables given to each preview. Their values are
	// readable by anyone who can read this source: no secrets.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Env []EnvVar `json:"env,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 40 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="name must be at most 40 lowercase letters, digits and hyphens: the previews of the source are named <name>-pr-<number>"
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`,priority=1
// +kubebuilder:printcolumn:name="Forks",type=string,JSONPath=`.spec.forkPullRequests`,priority=1

// PreviewSource allows a GitHub repository to have previews of its pull
// requests. It is cluster-scoped: an administrator of the cluster creates
// it, not the teams, since it lets a repository run code on the cluster.
type PreviewSource struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec is the repository and the settings of its previews
	// +required
	Spec PreviewSourceSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// PreviewSourceList contains a list of PreviewSource
type PreviewSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PreviewSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PreviewSource{}, &PreviewSourceList{})
		return nil
	})
}

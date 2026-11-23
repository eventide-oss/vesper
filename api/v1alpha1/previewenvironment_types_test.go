// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"reflect"
	"testing"
)

func TestComponentListSingleImage(t *testing.T) {
	spec := PreviewEnvironmentSpec{
		Image:    "ghcr.io/acme/app:pr-42",
		Port:     3000,
		Replicas: 2,
		Env:      []EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
	}
	want := []Component{{
		Name:     SingleComponentName,
		Image:    "ghcr.io/acme/app:pr-42",
		Port:     3000,
		Replicas: 2,
		Env:      []EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
		Expose:   true,
	}}
	if got := spec.ComponentList(); !reflect.DeepEqual(got, want) {
		t.Errorf("ComponentList() = %+v, want %+v", got, want)
	}
}

func TestComponentListComponents(t *testing.T) {
	components := []Component{
		{Name: "web", Image: "ghcr.io/acme/web:1", Port: 3000, Replicas: 1, Expose: true},
		{Name: "api", Image: "ghcr.io/acme/api:1", Port: 8080, Replicas: 1},
	}
	spec := PreviewEnvironmentSpec{Components: components, Port: 8080, Replicas: 1}
	if got := spec.ComponentList(); !reflect.DeepEqual(got, components) {
		t.Errorf("ComponentList() = %+v, want %+v", got, components)
	}
}

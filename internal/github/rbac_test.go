// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// TestRBACIsMinimal reads the ClusterRole of vesper-github: it holds exactly
// the rights ADR-0007 gives it, and none on Secrets or on what the operator
// manages. vesper-github faces the Internet: a right added here by mistake
// would be reachable from there.
func TestRBACIsMinimal(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "github", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(data, &role); err != nil {
		t.Fatalf("parse role.yaml: %v", err)
	}

	// "group/resource" -> verbs, sorted.
	want := map[string][]string{
		"vesper.eventide.dev/previewenvironments":   {"create", "delete", "get", "list", "patch", "update", "watch"},
		"vesper.eventide.dev/previewsources":        {"get", "list", "watch"},
		"authentication.k8s.io/tokenreviews":        {"create"},
		"authorization.k8s.io/subjectaccessreviews": {"create"},
	}
	got := map[string][]string{}
	for _, rule := range role.Rules {
		if len(rule.NonResourceURLs) > 0 || len(rule.ResourceNames) > 0 {
			t.Errorf("rule %+v: no non-resource URL or resource name expected", rule)
		}
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					if group == "*" || resource == "*" || verb == "*" {
						t.Errorf("rule %+v uses a wildcard", rule)
					}
					key := group + "/" + resource
					got[key] = append(got[key], verb)
				}
			}
		}
	}
	for key := range got {
		slices.Sort(got[key])
		if strings.Contains(key, "secrets") {
			t.Errorf("vesper-github has a right on Secrets: %s %v", key, got[key])
		}
	}
	for key, verbs := range want {
		if !slices.Equal(got[key], verbs) {
			t.Errorf("%s: verbs %v, want %v", key, got[key], verbs)
		}
	}
	for key, verbs := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected right: %s %v", key, verbs)
		}
	}
}

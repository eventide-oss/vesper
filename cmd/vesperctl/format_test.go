// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// fixedNow makes timestamps in the expected JSON and YAML stable.
var fixedNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// readyPreviewJSON is `get -o json` for readyPreview(fixedNow). It documents
// the contract of previewSummary: field names, order and types.
const readyPreviewJSON = `{
  "name": "pr-42",
  "namespace": "team-a",
  "runNamespace": "vesper-team-a-pr-42-3f9c2a1b",
  "createdAt": "2026-11-05T10:00:00Z",
  "phase": "Ready",
  "url": "https://pr-42.preview.example.com",
  "expiresAt": "2026-11-06T10:00:00Z",
  "spec": {
    "image": "ghcr.io/eventide/app:pr-42",
    "port": 8080,
    "replicas": 1,
    "ttl": "24h",
    "envCount": 2
  },
  "conditions": [
    {
      "type": "Available",
      "status": "True",
      "reason": "DeploymentReady",
      "message": "1/1 replicas available",
      "lastTransitionTime": "2026-11-05T10:30:00Z"
    },
    {
      "type": "Progressing",
      "status": "False",
      "reason": "Complete",
      "lastTransitionTime": "2026-11-05T10:30:00Z"
    }
  ]
}
`

const readyPreviewYAML = `conditions:
- lastTransitionTime: "2026-11-05T10:30:00Z"
  message: 1/1 replicas available
  reason: DeploymentReady
  status: "True"
  type: Available
- lastTransitionTime: "2026-11-05T10:30:00Z"
  reason: Complete
  status: "False"
  type: Progressing
createdAt: "2026-11-05T10:00:00Z"
expiresAt: "2026-11-06T10:00:00Z"
name: pr-42
namespace: team-a
phase: Ready
runNamespace: vesper-team-a-pr-42-3f9c2a1b
spec:
  envCount: 2
  image: ghcr.io/eventide/app:pr-42
  port: 8080
  replicas: 1
  ttl: 24h
url: https://pr-42.preview.example.com
`

// pendingPreview has no status yet; created at fixedNow.
func pendingPreview() runtime.Object {
	obj := newPreview("team-a", "pr-12", "", 0)
	obj.SetCreationTimestamp(metav1.NewTime(fixedNow))
	obj.Object["spec"] = map[string]any{"image": "nginx:1.27"}
	return obj
}

func TestOutputFormats(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		objs       []runtime.Object
		wantStdout string // full stdout, or a prefix for table output
		wantStderr string
	}{
		{
			name:       "get table",
			args:       []string{"get", "pr-42", "-n", "team-a", "-o", "table"},
			objs:       []runtime.Object{readyPreview(fixedNow)},
			wantStdout: "Name:        pr-42\nNamespace:   team-a\n",
		},
		{
			name:       "get json",
			args:       []string{"get", "pr-42", "-n", "team-a", "-o", "json"},
			objs:       []runtime.Object{readyPreview(fixedNow)},
			wantStdout: readyPreviewJSON,
		},
		{
			name:       "get yaml",
			args:       []string{"get", "pr-42", "-n", "team-a", "--output", "yaml"},
			objs:       []runtime.Object{readyPreview(fixedNow)},
			wantStdout: readyPreviewYAML,
		},
		{
			name: "get json, status not set yet",
			args: []string{"get", "pr-12", "-n", "team-a", "-o", "json"},
			objs: []runtime.Object{pendingPreview()},
			wantStdout: `{
  "name": "pr-12",
  "namespace": "team-a",
  "createdAt": "2026-11-05T12:00:00Z",
  "spec": {
    "image": "nginx:1.27",
    "envCount": 0
  }
}
`,
		},
		{
			name:       "list table",
			args:       []string{"list", "-A", "-o", "table"},
			objs:       []runtime.Object{readyPreview(fixedNow)},
			wantStdout: "NAMESPACE   NAME    PHASE   AGE\nteam-a      pr-42   Ready   ",
		},
		{
			name: "list json, sorted by namespace then name",
			args: []string{"list", "-A", "-o", "json"},
			objs: []runtime.Object{readyPreview(fixedNow), pendingPreview()},
			wantStdout: "{\n  \"items\": [\n" +
				`    {
      "name": "pr-12",
      "namespace": "team-a",
      "createdAt": "2026-11-05T12:00:00Z",
      "spec": {
        "image": "nginx:1.27",
        "envCount": 0
      }
    },
` + indent(strings.TrimSuffix(readyPreviewJSON, "\n"), "    ") + "\n  ]\n}\n",
		},
		{
			name:       "list yaml",
			args:       []string{"list", "-n", "team-a", "-o", "yaml"},
			objs:       []runtime.Object{pendingPreview()},
			wantStdout: "items:\n- createdAt: \"2026-11-05T12:00:00Z\"\n  name: pr-12\n  namespace: team-a\n  spec:\n    envCount: 0\n    image: nginx:1.27\n",
		},
		{
			name:       "empty list json is a valid document on stdout",
			args:       []string{"list", "-o", "json"},
			wantStdout: "{\n  \"items\": []\n}\n",
		},
		{
			name:       "empty list yaml is a valid document on stdout",
			args:       []string{"list", "-o", "yaml"},
			wantStdout: "items: []\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(tt.objs...)

			code, stdout, stderr := executeWith(t, tt.args, func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: client}
			})

			if code != exitOK {
				t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr)
			}
			if stderr != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr, tt.wantStderr)
			}
			if strings.Contains(strings.Join(tt.args, " "), "table") {
				if !strings.HasPrefix(stdout, tt.wantStdout) {
					t.Errorf("stdout =\n%s\nwant it to start with\n%s", stdout, tt.wantStdout)
				}
				return
			}
			if stdout != tt.wantStdout {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, tt.wantStdout)
			}
		})
	}
}

// indent prefixes every line of s.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func TestJSONOutputIsValidAndHidesEnv(t *testing.T) {
	for _, args := range [][]string{
		{"get", "pr-42", "-n", "team-a", "-o", "json"},
		{"list", "-A", "-o", "json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			client := newFakeClient(readyPreview(fixedNow))
			_, stdout, _ := executeWith(t, args, func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: client}
			})

			var doc map[string]any
			if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
			}
			for _, secret := range []string{"s3cr3t-value", "API_TOKEN", "LOG_LEVEL"} {
				if strings.Contains(stdout, secret) {
					t.Errorf("stdout contains %q from spec.env:\n%s", secret, stdout)
				}
			}
		})
	}
}

func TestUnknownOutputFormat(t *testing.T) {
	for _, args := range [][]string{
		{"get", "pr-42", "-o", "xml"},
		{"list", "--output=wide"},
	} {
		t.Run(args[0], func(t *testing.T) {
			code, stdout, stderr := executeWith(t, args, func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: newFakeClient()}
			})

			if code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want it empty", stdout)
			}
			if want := "valid formats are table, json, yaml"; !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		})
	}
}

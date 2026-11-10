// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testKubeconfig has two contexts on fake local servers: "dev" (the current
// context, with a namespace) and "prod" (without a namespace).
const testKubeconfig = `apiVersion: v1
kind: Config
current-context: dev
clusters:
- name: dev
  cluster:
    server: https://127.0.0.1:6443
- name: prod
  cluster:
    server: https://127.0.0.1:7443
contexts:
- name: dev
  context:
    cluster: dev
    user: tester
    namespace: team-a
- name: prod
  context:
    cluster: prod
    user: tester
users:
- name: tester
  user: {}
`

// writeFile writes content to a file named name in a temporary directory and
// returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKubeFactoryRestConfig(t *testing.T) {
	kubeconfig := writeFile(t, "config", testKubeconfig)

	tests := []struct {
		name     string
		opts     globalOptions
		env      map[string]string
		wantHost string
	}{
		{
			name:     "current context from --kubeconfig",
			opts:     globalOptions{Kubeconfig: kubeconfig},
			wantHost: "https://127.0.0.1:6443",
		},
		{
			name:     "current context from KUBECONFIG",
			env:      map[string]string{"KUBECONFIG": kubeconfig},
			wantHost: "https://127.0.0.1:6443",
		},
		{
			name:     "--context selects another context",
			opts:     globalOptions{Kubeconfig: kubeconfig, Context: "prod"},
			wantHost: "https://127.0.0.1:7443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			opts.getenv = fakeEnv(tt.env)
			f := newKubeFactory(&opts)

			cfg, err := f.restConfig()
			if err != nil {
				t.Fatalf("restConfig() error = %v", err)
			}
			if cfg.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", cfg.Host, tt.wantHost)
			}
			if cfg.Timeout != requestTimeout {
				t.Errorf("Timeout = %v, want %v", cfg.Timeout, requestTimeout)
			}
			if !strings.HasPrefix(cfg.UserAgent, "vesperctl/") {
				t.Errorf("UserAgent = %q, want a vesperctl/ prefix", cfg.UserAgent)
			}

			client, err := f.DynamicClient()
			if err != nil {
				t.Fatalf("DynamicClient() error = %v", err)
			}
			if client == nil {
				t.Fatal("DynamicClient() returned a nil client")
			}
		})
	}
}

func TestKubeFactoryNamespace(t *testing.T) {
	kubeconfig := writeFile(t, "config", testKubeconfig)

	tests := []struct {
		name string
		opts globalOptions
		want string
	}{
		{
			name: "namespace of the current context",
			opts: globalOptions{Kubeconfig: kubeconfig},
			want: "team-a",
		},
		{
			name: "--namespace wins over the context",
			opts: globalOptions{Kubeconfig: kubeconfig, Namespace: "team-b"},
			want: "team-b",
		},
		{
			name: "context without a namespace falls back to default",
			opts: globalOptions{Kubeconfig: kubeconfig, Context: "prod"},
			want: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			opts.getenv = emptyEnv

			got, err := newKubeFactory(&opts).Namespace()
			if err != nil {
				t.Fatalf("Namespace() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Namespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKubeFactoryErrors(t *testing.T) {
	kubeconfig := writeFile(t, "config", testKubeconfig)
	invalid := writeFile(t, "invalid", "this is: [not a kubeconfig")

	tests := []struct {
		name    string
		opts    globalOptions
		env     map[string]string
		wantErr string
	}{
		{
			name:    "invalid kubeconfig file",
			opts:    globalOptions{Kubeconfig: invalid},
			wantErr: "load kubeconfig:",
		},
		{
			name:    "missing --kubeconfig file",
			opts:    globalOptions{Kubeconfig: filepath.Join(t.TempDir(), "missing")},
			wantErr: "load kubeconfig:",
		},
		{
			name:    "unknown context",
			opts:    globalOptions{Kubeconfig: kubeconfig, Context: "nope"},
			wantErr: "nope",
		},
		{
			name:    "no kubeconfig at all",
			env:     map[string]string{"HOME": t.TempDir()},
			wantErr: "no kubeconfig found: set --kubeconfig or the KUBECONFIG environment variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			opts.getenv = fakeEnv(tt.env)

			_, err := newKubeFactory(&opts).DynamicClient()
			if err == nil {
				t.Fatal("DynamicClient() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("DynamicClient() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

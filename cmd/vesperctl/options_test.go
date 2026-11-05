package main

import (
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeEnv returns a getenv function backed by a map.
func fakeEnv(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// parseGlobalOptions runs the root command with args and returns the global
// options it filled.
func parseGlobalOptions(t *testing.T, args []string, env map[string]string) *globalOptions {
	t.Helper()

	opts := newGlobalOptions(fakeEnv(env))
	root := newRootCmd(io.Discard, io.Discard, opts, newKubeFactory(opts))
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(%q) error = %v", args, err)
	}
	return opts
}

func TestGlobalFlagsAreParsed(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantContext   string
		wantNamespace string
	}{
		{
			name: "defaults are empty",
			args: []string{"version"},
		},
		{
			name:          "long flags before the subcommand",
			args:          []string{"--context", "kind-vesper-dev", "--namespace", "team-a", "version"},
			wantContext:   "kind-vesper-dev",
			wantNamespace: "team-a",
		},
		{
			name:          "flags after the subcommand are inherited",
			args:          []string{"version", "--context=prod", "-n", "team-b"},
			wantContext:   "prod",
			wantNamespace: "team-b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := parseGlobalOptions(t, tt.args, nil)

			if opts.Context != tt.wantContext {
				t.Errorf("Context = %q, want %q", opts.Context, tt.wantContext)
			}
			if opts.Namespace != tt.wantNamespace {
				t.Errorf("Namespace = %q, want %q", opts.Namespace, tt.wantNamespace)
			}
		})
	}
}

func TestKubeconfigPaths(t *testing.T) {
	home := filepath.Join("home", "alice")
	a := filepath.Join("etc", "a.yaml")
	b := filepath.Join("etc", "b.yaml")
	flagPath := filepath.Join("tmp", "flag.yaml")
	list := func(paths ...string) string {
		return strings.Join(paths, string(filepath.ListSeparator))
	}

	tests := []struct {
		name           string
		args           []string
		env            map[string]string
		wantExplicit   string
		wantPrecedence []string
	}{
		{
			name:         "flag wins over KUBECONFIG",
			args:         []string{"--kubeconfig", flagPath, "version"},
			env:          map[string]string{"KUBECONFIG": a, "HOME": home},
			wantExplicit: flagPath,
		},
		{
			name:           "KUBECONFIG wins over the default",
			args:           []string{"version"},
			env:            map[string]string{"KUBECONFIG": a, "HOME": home},
			wantPrecedence: []string{a},
		},
		{
			name:           "KUBECONFIG with several paths, empty entries ignored",
			args:           []string{"version"},
			env:            map[string]string{"KUBECONFIG": list(a, "", b)},
			wantPrecedence: []string{a, b},
		},
		{
			name:           "default from HOME",
			args:           []string{"version"},
			env:            map[string]string{"HOME": home},
			wantPrecedence: []string{filepath.Join(home, ".kube", "config")},
		},
		{
			name:           "default from USERPROFILE when HOME is unset",
			args:           []string{"version"},
			env:            map[string]string{"USERPROFILE": home},
			wantPrecedence: []string{filepath.Join(home, ".kube", "config")},
		},
		{
			name: "no flag, no KUBECONFIG, no home",
			args: []string{"version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := parseGlobalOptions(t, tt.args, tt.env)

			explicit, precedence := opts.kubeconfigPaths()

			if explicit != tt.wantExplicit {
				t.Errorf("explicit = %q, want %q", explicit, tt.wantExplicit)
			}
			if !slices.Equal(precedence, tt.wantPrecedence) {
				t.Errorf("precedence = %q, want %q", precedence, tt.wantPrecedence)
			}
		})
	}
}

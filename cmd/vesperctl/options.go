package main

import (
	"path/filepath"

	"github.com/spf13/pflag"
)

// globalOptions holds the flags shared by every command. Commands receive a
// pointer to it instead of reading flags, environment variables or other
// global state themselves.
//
// Empty Context and Namespace mean "use the current context of the
// kubeconfig" and "use the namespace of that context": they are resolved
// later, when the kubeconfig is loaded.
type globalOptions struct {
	Kubeconfig string
	Context    string
	Namespace  string

	// getenv reads an environment variable. It is os.Getenv in production
	// and a fake in tests.
	getenv func(string) string
}

func newGlobalOptions(getenv func(string) string) *globalOptions {
	return &globalOptions{getenv: getenv}
}

// addFlags registers the global flags on fs, which is meant to be the
// persistent flag set of the root command so that every subcommand inherits
// them.
func (o *globalOptions) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Kubeconfig, "kubeconfig", "",
		"path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	fs.StringVar(&o.Context, "context", "",
		"name of the kubeconfig context to use (default: the current context)")
	fs.StringVarP(&o.Namespace, "namespace", "n", "",
		"namespace to use (default: the namespace of the context)")
}

// kubeconfigPaths returns where to load the kubeconfig from, with the same
// precedence as kubectl:
//
//  1. the --kubeconfig flag, returned as explicit;
//  2. otherwise the paths listed in $KUBECONFIG, returned as precedence;
//  3. otherwise ~/.kube/config, returned as precedence.
//
// explicit and precedence match the ExplicitPath and Precedence fields of
// client-go's clientcmd.ClientConfigLoadingRules. precedence is nil when no
// home directory is known.
func (o *globalOptions) kubeconfigPaths() (explicit string, precedence []string) {
	if o.Kubeconfig != "" {
		return o.Kubeconfig, nil
	}

	// KUBECONFIG may hold several paths, separated by ":" (";" on Windows).
	for _, p := range filepath.SplitList(o.getenv("KUBECONFIG")) {
		if p != "" {
			precedence = append(precedence, p)
		}
	}
	if len(precedence) > 0 {
		return "", precedence
	}

	if home := o.homeDir(); home != "" {
		return "", []string{filepath.Join(home, ".kube", "config")}
	}
	return "", nil
}

// homeDir returns the home directory from the environment: HOME on Unix,
// USERPROFILE on Windows. It does not call os.UserHomeDir so that tests can
// control the result.
func (o *globalOptions) homeDir() string {
	if home := o.getenv("HOME"); home != "" {
		return home
	}
	return o.getenv("USERPROFILE")
}

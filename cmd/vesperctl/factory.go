// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// requestTimeout bounds every request sent to the API server, so that an
// unreachable cluster fails instead of hanging.
const requestTimeout = 30 * time.Second

// clientFactory builds what cluster-facing commands need. Commands receive it
// by injection: production code uses kubeFactory, tests use a fake that
// returns client-go's fake dynamic client.
type clientFactory interface {
	// DynamicClient returns a client for any resource, including custom
	// resources whose Go types are not generated.
	DynamicClient() (dynamic.Interface, error)

	// Namespace returns the namespace to work in: the --namespace flag, else
	// the namespace of the selected context, else "default".
	Namespace() (string, error)
}

// kubeFactory is the clientFactory backed by a kubeconfig, loaded with
// client-go's clientcmd as kubectl does.
type kubeFactory struct {
	opts *globalOptions
}

// newKubeFactory returns a factory that reads opts when a client is
// requested, that is after the flags have been parsed.
func newKubeFactory(opts *globalOptions) *kubeFactory {
	return &kubeFactory{opts: opts}
}

// clientConfig describes how to load the kubeconfig. Nothing is read from
// disk until one of its methods is called.
func (f *kubeFactory) clientConfig() clientcmd.ClientConfig {
	explicit, precedence := f.opts.kubeconfigPaths()
	rules := &clientcmd.ClientConfigLoadingRules{
		ExplicitPath: explicit,
		Precedence:   precedence,
	}
	overrides := &clientcmd.ConfigOverrides{
		CurrentContext: f.opts.Context,
		Context:        clientcmdapi.Context{Namespace: f.opts.Namespace},
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
}

// restConfig returns the connection settings for the API server: address,
// credentials and timeout.
func (f *kubeFactory) restConfig() (*rest.Config, error) {
	cfg, err := f.clientConfig().ClientConfig()
	if err != nil {
		return nil, kubeconfigError(err)
	}
	cfg.Timeout = requestTimeout
	cfg.UserAgent = "vesperctl/" + version
	return cfg, nil
}

func (f *kubeFactory) DynamicClient() (dynamic.Interface, error) {
	cfg, err := f.restConfig()
	if err != nil {
		return nil, err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return client, nil
}

func (f *kubeFactory) Namespace() (string, error) {
	ns, _, err := f.clientConfig().Namespace()
	if err != nil {
		return "", kubeconfigError(err)
	}
	return ns, nil
}

// kubeconfigError turns a clientcmd error into a message that tells the user
// what to do.
func kubeconfigError(err error) error {
	if clientcmd.IsEmptyConfig(err) {
		return errors.New("no kubeconfig found: set --kubeconfig or the KUBECONFIG environment variable")
	}
	return fmt.Errorf("load kubeconfig: %w", err)
}

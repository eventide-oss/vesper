package main

import (
	"errors"
	"fmt"
	"net/url"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// errCRDNotInstalled is returned when the API server does not know the
// PreviewEnvironment resource: Vesper is not installed in the cluster.
var errCRDNotInstalled = fmt.Errorf("the PreviewEnvironment API (%s) is not installed in this cluster: "+
	"install Vesper, or check --context", previewEnvironmentGVR.GroupResource())

// apiError turns an error returned by a request to the API server into a
// message the user can act on. action describes the request, for example
// `list preview environments in namespace "team-a"`.
//
// Errors that are not recognized keep their original message, after action.
func apiError(err error, action string) error {
	switch {
	case apierrors.IsForbidden(err):
		return fmt.Errorf("you are not allowed to %s: ask a cluster admin for access (RBAC)", action)
	case apierrors.IsUnauthorized(err):
		return errors.New("the API server rejected your credentials: check your kubeconfig, or log in again")
	}

	// A *url.Error means the request got no HTTP response: the server is
	// unreachable, timed out, or the TLS handshake failed. The cause is kept
	// because it tells which one.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("cannot reach the API server at %s (check --context and your network): %w",
			serverAddress(urlErr.URL), urlErr.Err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

// serverAddress keeps the scheme and host of a request URL: the path and the
// query say nothing useful to the user.
func serverAddress(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

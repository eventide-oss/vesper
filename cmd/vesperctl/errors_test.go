package main

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// timeoutError is a net.Error that reports a timeout, as the HTTP client
// does when requestTimeout elapses.
type timeoutError struct{}

func (timeoutError) Error() string   { return "Client.Timeout exceeded while awaiting headers" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestAPIErrors(t *testing.T) {
	gr := previewEnvironmentGVR.GroupResource()
	requestURL := "https://10.0.0.1:6443/apis/vesper.eventide.dev/v1alpha1/namespaces/default/previewenvironments"

	tests := []struct {
		name string
		err  error
		// want is the expected stderr for `list` and `get pr-1`, in that order.
		want [2]string
	}{
		{
			name: "forbidden",
			err:  apierrors.NewForbidden(gr, "", errors.New("RBAC: access denied")),
			want: [2]string{
				`vesperctl: you are not allowed to list preview environments in namespace "default": ` +
					"ask a cluster admin for access (RBAC)\n",
				`vesperctl: you are not allowed to get preview environment "pr-1" in namespace "default": ` +
					"ask a cluster admin for access (RBAC)\n",
			},
		},
		{
			name: "unauthorized",
			err:  apierrors.NewUnauthorized("Unauthorized"),
			want: [2]string{
				"vesperctl: the API server rejected your credentials: check your kubeconfig, or log in again\n",
				"vesperctl: the API server rejected your credentials: check your kubeconfig, or log in again\n",
			},
		},
		{
			name: "connection refused",
			err: &url.Error{Op: "Get", URL: requestURL, Err: &net.OpError{
				Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
			}},
			want: [2]string{
				"vesperctl: cannot reach the API server at https://10.0.0.1:6443 (check --context and your network): " +
					"dial tcp: connect: connection refused\n",
				"vesperctl: cannot reach the API server at https://10.0.0.1:6443 (check --context and your network): " +
					"dial tcp: connect: connection refused\n",
			},
		},
		{
			name: "timeout",
			err:  &url.Error{Op: "Get", URL: requestURL, Err: timeoutError{}},
			want: [2]string{
				"vesperctl: cannot reach the API server at https://10.0.0.1:6443 (check --context and your network): " +
					"Client.Timeout exceeded while awaiting headers\n",
				"vesperctl: cannot reach the API server at https://10.0.0.1:6443 (check --context and your network): " +
					"Client.Timeout exceeded while awaiting headers\n",
			},
		},
		{
			name: "other errors keep their message",
			err:  apierrors.NewInternalError(errors.New("etcd is unavailable")),
			want: [2]string{
				`vesperctl: list preview environments in namespace "default": ` +
					"Internal error occurred: etcd is unavailable\n",
				`vesperctl: get preview environment "pr-1" in namespace "default": ` +
					"Internal error occurred: etcd is unavailable\n",
			},
		},
	}

	for _, tt := range tests {
		for i, args := range [][]string{{"list"}, {"get", "pr-1"}} {
			t.Run(tt.name+"/"+args[0], func(t *testing.T) {
				client := newFakeClient()
				client.PrependReactor("*", "previewenvironments",
					func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, tt.err })

				code, stdout, stderr := executeWith(t, args, func(opts *globalOptions) clientFactory {
					return &fakeFactory{opts: opts, client: client}
				})

				if code != exitFailure {
					t.Errorf("exit code = %d, want %d", code, exitFailure)
				}
				if stdout != "" {
					t.Errorf("stdout = %q, want it empty", stdout)
				}
				if stderr != tt.want[i] {
					t.Errorf("stderr =\n%q\nwant\n%q", stderr, tt.want[i])
				}
			})
		}
	}
}

func TestListAllNamespacesForbidden(t *testing.T) {
	client := newFakeClient()
	client.PrependReactor("list", "previewenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(previewEnvironmentGVR.GroupResource(), "", errors.New("denied"))
	})

	_, _, stderr := executeWith(t, []string{"list", "-A"}, func(opts *globalOptions) clientFactory {
		return &fakeFactory{opts: opts, client: client}
	})

	if want := "you are not allowed to list preview environments in all namespaces"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestServerAddress(t *testing.T) {
	tests := map[string]string{
		"https://10.0.0.1:6443/apis/x?limit=500": "https://10.0.0.1:6443",
		"http://localhost:8080":                  "http://localhost:8080",
		"not a url":                              "not a url",
	}
	for raw, want := range tests {
		if got := serverAddress(raw); got != want {
			t.Errorf("serverAddress(%q) = %q, want %q", raw, got, want)
		}
	}
}

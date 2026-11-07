package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

const question = `Delete preview environment "pr-42" in namespace "team-a", ` +
	"with everything in its preview namespace? [y/N] "

// executeDelete runs vesperctl with args against client, with stdin as the
// standard input.
func executeDelete(t *testing.T, client *dynamicfake.FakeDynamicClient, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts := newGlobalOptions(emptyEnv)
	root := newRootCmd(&stdout, &stderr, opts, &fakeFactory{opts: opts, client: client})
	root.SetIn(strings.NewReader(stdin))
	code := execute(root, args, &stderr)
	return code, stdout.String(), stderr.String()
}

// deletablePreview returns the preview team-a/pr-42 with a UID.
func deletablePreview() *unstructured.Unstructured {
	obj := newPreview("team-a", "pr-42", "Ready", time.Hour)
	obj.SetUID(types.UID("uid-42"))
	return obj
}

// deletions returns the delete requests client received.
func deletions(client *dynamicfake.FakeDynamicClient) []k8stesting.DeleteAction {
	var out []k8stesting.DeleteAction
	for _, a := range client.Actions() {
		if d, ok := a.(k8stesting.DeleteAction); ok {
			out = append(out, d)
		}
	}
	return out
}

// exists reports whether team-a/pr-42 is still in client.
func exists(t *testing.T, client *dynamicfake.FakeDynamicClient) bool {
	t.Helper()
	_, err := client.Resource(previewEnvironmentGVR).Namespace("team-a").
		Get(context.Background(), "pr-42", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get preview: %v", err)
	}
	return err == nil
}

const deleted = "preview environment \"pr-42\" is being deleted: " +
	"the operator deletes its namespace, then the preview\n"

func TestDeleteConfirmed(t *testing.T) {
	for _, answer := range []string{"y\n", "yes\n", "Y\n", " YES \n", "y"} {
		t.Run(answer, func(t *testing.T) {
			client := newFakeClient(deletablePreview())
			code, stdout, stderr := executeDelete(t, client, answer, "delete", "pr-42", "-n", "team-a")
			if code != exitOK || stdout != deleted || stderr != question {
				t.Errorf("exit code %d, stdout %q, stderr %q; want 0, %q and the question", code, stdout, stderr, deleted)
			}
			if exists(t, client) {
				t.Error("the preview still exists")
			}
		})
	}
}

// TestDeleteSendsUID checks that the deletion is conditional on the UID of
// the preview that was confirmed.
func TestDeleteSendsUID(t *testing.T) {
	client := newFakeClient(deletablePreview())
	if code, _, stderr := executeDelete(t, client, "", "delete", "pr-42", "-n", "team-a", "--yes"); code != exitOK {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	d := deletions(client)
	if len(d) != 1 {
		t.Fatalf("deletions = %v, want 1", d)
	}
	if p := d[0].GetDeleteOptions().Preconditions; p == nil || p.UID == nil || *p.UID != "uid-42" {
		t.Errorf("preconditions = %+v, want the UID uid-42", p)
	}
}

func TestDeleteYes(t *testing.T) {
	for _, flag := range []string{"--yes", "-y"} {
		t.Run(flag, func(t *testing.T) {
			client := newFakeClient(deletablePreview())
			code, stdout, stderr := executeDelete(t, client, "", "delete", "pr-42", "-n", "team-a", flag)
			if code != exitOK || stdout != deleted || stderr != "" {
				t.Errorf("exit code %d, stdout %q, stderr %q; want 0, %q and no question", code, stdout, stderr, deleted)
			}
			if exists(t, client) {
				t.Error("the preview still exists")
			}
		})
	}
}

func TestDeleteAborted(t *testing.T) {
	tests := []struct{ name, answer, want string }{
		{"no", "n\n", question + "vesperctl: not deleted\n"},
		{"empty line", "\n", question + "vesperctl: not deleted\n"},
		{"anything else", "sure\n", question + "vesperctl: not deleted\n"},
		{"no input", "", question + "\nvesperctl: no answer: not deleted; pass --yes to delete without confirmation\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(deletablePreview())
			code, stdout, stderr := executeDelete(t, client, tt.answer, "delete", "pr-42", "-n", "team-a")
			if code != exitFailure || stdout != "" || stderr != tt.want {
				t.Errorf("exit code %d, stdout %q, stderr %q; want 1 and %q", code, stdout, stderr, tt.want)
			}
			if d := deletions(client); len(d) != 0 || !exists(t, client) {
				t.Errorf("deletions = %v, want none", d)
			}
		})
	}
}

func TestDeleteAlreadyDeleting(t *testing.T) {
	preview := deletablePreview()
	preview.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
	preview.SetFinalizers([]string{"vesper.eventide.dev/cleanup"})
	client := newFakeClient(preview)

	code, stdout, stderr := executeDelete(t, client, "", "delete", "pr-42", "-n", "team-a")
	if want := "preview environment \"pr-42\" is already being deleted\n"; code != exitOK || stdout != want || stderr != "" {
		t.Errorf("exit code %d, stdout %q, stderr %q; want 0, %q and no question", code, stdout, stderr, want)
	}
	if d := deletions(client); len(d) != 0 {
		t.Errorf("deletions = %v, want none", d)
	}
}

func TestDeleteErrors(t *testing.T) {
	gr := previewEnvironmentGVR.GroupResource()
	tests := []struct {
		name      string
		args      []string
		deleteErr error
		wantCode  int
		want      string
	}{
		{"not found", []string{"pr-7", "--yes"}, nil, exitFailure,
			"vesperctl: preview environment \"pr-7\" not found in namespace \"team-a\"\n"},
		{"deleted meanwhile", []string{"pr-42", "--yes"}, apierrors.NewNotFound(gr, "pr-42"), exitFailure,
			"vesperctl: preview environment \"pr-42\" not found in namespace \"team-a\"\n"},
		{"created again meanwhile", []string{"pr-42", "--yes"},
			apierrors.NewConflict(gr, "pr-42", errors.New("precondition failed: UID")), exitFailure,
			"vesperctl: preview environment \"pr-42\" in namespace \"team-a\" changed since it was read: " +
				"not deleted; run the command again\n"},
		{"forbidden", []string{"pr-42", "--yes"}, apierrors.NewForbidden(gr, "pr-42", errors.New("denied")),
			exitFailure, "vesperctl: you are not allowed to delete preview environment \"pr-42\" in namespace " +
				"\"team-a\": ask a cluster admin for access (RBAC)\n"},
		{"no name", []string{"--yes"}, nil, exitUsage, "accepts 1 arg(s), received 0"},
		{"two names", []string{"pr-42", "pr-7"}, nil, exitUsage, "accepts 1 arg(s), received 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(deletablePreview())
			if tt.deleteErr != nil {
				client.PrependReactor("delete", "previewenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.deleteErr
				})
			}
			args := append([]string{"delete", "-n", "team-a"}, tt.args...)
			code, stdout, stderr := executeDelete(t, client, "", args...)
			if code != tt.wantCode || stdout != "" || !strings.HasPrefix(stderr, "vesperctl: ") ||
				!strings.Contains(stderr, tt.want) {
				t.Errorf("exit code %d, stdout %q, stderr %q; want %d and %q", code, stdout, stderr, tt.wantCode, tt.want)
			}
		})
	}
}

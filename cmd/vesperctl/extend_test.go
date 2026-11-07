package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// created is the creation time of the previews of these tests.
var created = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

// previewWithTTL returns the preview team-a/pr-42 created at created, with
// ttl as spec.ttl; an empty ttl leaves it unset.
func previewWithTTL(ttl string) *unstructured.Unstructured {
	obj := newPreview("team-a", "pr-42", "Ready", 0)
	obj.SetCreationTimestamp(metav1.NewTime(created))
	obj.SetResourceVersion("1")
	if ttl != "" {
		_ = unstructured.SetNestedField(obj.Object, ttl, "spec", "ttl")
	}
	return obj
}

// specTTL returns spec.ttl of team-a/pr-42 in client.
func specTTL(t *testing.T, client *dynamicfake.FakeDynamicClient) string {
	t.Helper()
	obj, err := client.Resource(previewEnvironmentGVR).Namespace("team-a").
		Get(context.Background(), "pr-42", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get preview: %v", err)
	}
	ttl, _, _ := unstructured.NestedString(obj.Object, "spec", "ttl")
	return ttl
}

func TestExtend(t *testing.T) {
	tests := []struct {
		name, ttl, by, wantTTL, wantOut string
	}{
		{"from the ttl", "24h0m0s", "2h", "26h0m0s",
			`preview environment "pr-42" extended by 2h0m0s: ttl 26h0m0s, expires at 2026-11-08T12:00:00Z`},
		{"no ttl: the default 24h", "", "90m", "25h30m0s",
			`preview environment "pr-42" extended by 1h30m0s: ttl 25h30m0s, expires at 2026-11-08T11:30:00Z`},
		{"up to the maximum", "100h", "68h", "168h0m0s",
			`preview environment "pr-42" extended by 68h0m0s: ttl 168h0m0s, expires at 2026-11-14T10:00:00Z`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(previewWithTTL(tt.ttl))
			code, stdout, stderr := executeCreate(t, client, "extend", "pr-42", "-n", "team-a", "--by", tt.by)
			if code != exitOK {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr)
			}
			if stdout != tt.wantOut+"\n" {
				t.Errorf("stdout = %q, want %q", stdout, tt.wantOut+"\n")
			}
			if got := specTTL(t, client); got != tt.wantTTL {
				t.Errorf("spec.ttl = %q, want %q", got, tt.wantTTL)
			}
		})
	}
}

// TestExtendSendsResourceVersion checks that the patch is conditional on the
// version read, so that a concurrent change is not overwritten.
func TestExtendSendsResourceVersion(t *testing.T) {
	client := newFakeClient(previewWithTTL("24h"))
	if code, _, stderr := executeCreate(t, client, "extend", "pr-42", "-n", "team-a", "--by", "1h"); code != exitOK {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	var patches []string
	for _, a := range client.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok {
			patches = append(patches, string(p.GetPatch()))
		}
	}
	want := `{"metadata":{"resourceVersion":"1"},"spec":{"ttl":"25h0m0s"}}`
	if len(patches) != 1 || patches[0] != want {
		t.Errorf("patches = %q, want [%q]", patches, want)
	}
}

// TestExtendRetriesOnConflict checks that a preview changed between the read
// and the patch is read again: the other change is kept.
func TestExtendRetriesOnConflict(t *testing.T) {
	client := newFakeClient(previewWithTTL("24h"))
	conflicted := false
	client.PrependReactor("patch", "previewenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicted {
			return false, nil, nil
		}
		conflicted = true
		// Someone else extends the preview first. The tracker is written
		// directly: the client is locked while a reactor runs.
		obj := previewWithTTL("30h")
		obj.SetResourceVersion("2")
		if err := client.Tracker().Update(previewEnvironmentGVR, obj, "team-a"); err != nil {
			t.Errorf("concurrent update: %v", err)
		}
		return true, nil, apierrors.NewConflict(previewEnvironmentGVR.GroupResource(), "pr-42", errors.New("changed"))
	})

	code, stdout, stderr := executeCreate(t, client, "extend", "pr-42", "-n", "team-a", "--by", "2h")
	if code != exitOK {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if got := specTTL(t, client); got != "32h0m0s" {
		t.Errorf("spec.ttl = %q, want 32h0m0s: the concurrent extension is lost", got)
	}
	if !strings.Contains(stdout, "ttl 32h0m0s") {
		t.Errorf("stdout = %q, want the ttl 32h0m0s", stdout)
	}
}

// TestExtendRefused checks the extensions vesperctl refuses before any
// patch.
func TestExtendRefused(t *testing.T) {
	deleting := previewWithTTL("24h")
	deleting.SetDeletionTimestamp(&metav1.Time{Time: created.Add(time.Hour)})
	deleting.SetFinalizers([]string{"vesper.eventide.dev/cleanup"})

	tests := []struct {
		name      string
		preview   *unstructured.Unstructured
		args      []string
		wantCode  int
		wantUsage bool
		want      string
	}{
		{"over the maximum", previewWithTTL("24h"), []string{"pr-42", "--by", "145h"}, exitUsage, false,
			`cannot extend "pr-42" by 145h0m0s: its ttl would be 169h0m0s, over the maximum of 168h0m0s; ` +
				"it can be extended by 144h0m0s at most"},
		{"already at the maximum", previewWithTTL("168h"), []string{"pr-42", "--by", "1m"}, exitUsage, false,
			"it can be extended by 0s at most"},
		{"huge --by", previewWithTTL("24h"), []string{"pr-42", "--by", "2562047h"}, exitUsage, false,
			"over the maximum of 168h0m0s"},
		{"being deleted", deleting, []string{"pr-42", "--by", "1h"}, exitFailure, false,
			`preview environment "pr-42" is being deleted: it cannot be extended; create it again`},
		{"not found", previewWithTTL("24h"), []string{"pr-7", "--by", "1h"}, exitFailure, false,
			`preview environment "pr-7" not found in namespace "team-a"`},
		{"no --by", previewWithTTL("24h"), []string{"pr-42"}, exitUsage, true, "--by is required"},
		{"--by zero", previewWithTTL("24h"), []string{"pr-42", "--by", "0s"}, exitUsage, true,
			"--by must be a positive duration, got 0s"},
		{"--by negative", previewWithTTL("24h"), []string{"pr-42", "--by", "-1h"}, exitUsage, true,
			"--by must be a positive duration, got -1h0m0s"},
		{"--by not a duration", previewWithTTL("24h"), []string{"pr-42", "--by", "2 days"}, exitUsage, true,
			`invalid argument "2 days"`},
		{"no name", previewWithTTL("24h"), []string{"--by", "1h"}, exitUsage, true, "accepts 1 arg(s), received 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(tt.preview)
			args := append([]string{"extend", "-n", "team-a"}, tt.args...)
			code, stdout, stderr := executeCreate(t, client, args...)
			if code != tt.wantCode || stdout != "" || !strings.Contains(stderr, tt.want) ||
				strings.Contains(stderr, "Usage:") != tt.wantUsage {
				t.Errorf("exit code %d, stdout %q, stderr %q; want %d and %q, usage %v",
					code, stdout, stderr, tt.wantCode, tt.want, tt.wantUsage)
			}
			for _, a := range client.Actions() {
				if a.GetVerb() == "patch" {
					t.Errorf("patch sent: %v", a)
				}
			}
		})
	}
}

// TestExtendAPIErrors checks the answers of the API server to the patch.
func TestExtendAPIErrors(t *testing.T) {
	gk := schema.GroupKind{Group: previewEnvironmentGVR.Group, Kind: "PreviewEnvironment"}
	gr := previewEnvironmentGVR.GroupResource()
	tests := []struct {
		name     string
		err      error
		wantCode int
		want     string
	}{
		{"invalid", apierrors.NewInvalid(gk, "pr-42", field.ErrorList{
			field.Invalid(field.NewPath("spec", "ttl"), "169h", "ttl must be between 1m and 168h"),
		}), exitUsage, "vesperctl: the API server rejected the new ttl: " +
			"spec.ttl: Invalid value: \"169h\": ttl must be between 1m and 168h\n"},
		{"deleted meanwhile", apierrors.NewNotFound(gr, "pr-42"), exitFailure,
			"vesperctl: preview environment \"pr-42\" not found in namespace \"team-a\"\n"},
		{"keeps changing", apierrors.NewConflict(gr, "pr-42", errors.New("changed")), exitFailure,
			"vesperctl: preview environment \"pr-42\" in namespace \"team-a\" keeps changing: try again\n"},
		{"forbidden", apierrors.NewForbidden(gr, "pr-42", errors.New("denied")), exitFailure,
			"vesperctl: you are not allowed to extend preview environment \"pr-42\" in namespace \"team-a\": " +
				"ask a cluster admin for access (RBAC)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(previewWithTTL("24h"))
			client.PrependReactor("patch", "previewenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, tt.err
			})
			code, stdout, stderr := executeCreate(t, client, "extend", "pr-42", "-n", "team-a", "--by", "1h")
			if code != tt.wantCode || stdout != "" || stderr != tt.want {
				t.Errorf("exit code %d, stdout %q, stderr %q; want %d and %q", code, stdout, stderr, tt.wantCode, tt.want)
			}
		})
	}
}

// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eventide-oss/vesper/internal/github/githubtest"
)

const (
	routeToken        = "POST /app/installations/{id}/access_tokens" //nolint:gosec // a route, not a credential
	routeInstallation = "GET /repos/{owner}/{name}/installation"
	routeListPulls    = "GET /repos/{owner}/{name}/pulls"
	sha1              = "1111111111111111111111111111111111111111"
	sha2              = "2222222222222222222222222222222222222222"
)

// newTestClient returns a fake GitHub with acme/app installed, and a Client
// of the App it knows.
func newTestClient(t *testing.T) (*githubtest.Server, *Client) {
	t.Helper()
	server := githubtest.New(t)
	server.AddRepo("acme/app", 7)
	app, err := NewApp(server.AppID, server.KeyPEM, server.URL, "vesper-github/test")
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return server, NewClient(app)
}

func TestPullRequest(t *testing.T) {
	server, client := newTestClient(t)
	server.SetPullRequest("acme/app", githubtest.PullRequest{Number: 1, HeadSHA: sha1, Labels: []string{"no-preview", "bug"}})
	server.SetPullRequest("acme/app", githubtest.PullRequest{Number: 2, HeadSHA: sha2, Draft: true})
	server.SetPullRequest("acme/app", githubtest.PullRequest{Number: 3, HeadSHA: sha1, HeadRepo: "stranger/app"})
	server.SetPullRequest("acme/app", githubtest.PullRequest{Number: 4, HeadSHA: sha1, HeadRepo: "-"})
	server.SetPullRequest("acme/app", githubtest.PullRequest{Number: 5, HeadSHA: sha1, State: "closed"})

	tests := []struct {
		number int
		want   PullRequest
	}{
		{1, PullRequest{Number: 1, Open: true, HeadSHA: sha1, Labels: []string{"no-preview", "bug"}}},
		{2, PullRequest{Number: 2, Open: true, Draft: true, HeadSHA: sha2, Labels: []string{}}},
		{3, PullRequest{Number: 3, Open: true, HeadSHA: sha1, Fork: true, Labels: []string{}}},
		{4, PullRequest{Number: 4, Open: true, HeadSHA: sha1, Fork: true, Labels: []string{}}},
		{5, PullRequest{Number: 5, HeadSHA: sha1, Labels: []string{}}},
	}
	for _, tt := range tests {
		// GitHub compares repository names without case.
		got, err := client.PullRequest(context.Background(), "Acme/App", tt.number)
		if err != nil {
			t.Fatalf("PullRequest(%d): %v", tt.number, err)
		}
		if got.Number != tt.want.Number || got.Open != tt.want.Open || got.Draft != tt.want.Draft ||
			got.HeadSHA != tt.want.HeadSHA || got.Fork != tt.want.Fork || !slices.Equal(got.Labels, tt.want.Labels) {
			t.Errorf("PullRequest(%d) = %+v, want %+v", tt.number, got, tt.want)
		}
	}
	if !(PullRequest{Labels: []string{"preview"}}).HasLabel("preview") || (PullRequest{}).HasLabel("preview") {
		t.Error("HasLabel")
	}

	// One installation lookup and one token for all the requests.
	if n := server.Requests(routeInstallation); n != 1 {
		t.Errorf("%d installation lookups, want 1", n)
	}
	if n := server.Requests(routeToken); n != 1 {
		t.Errorf("%d tokens created, want 1", n)
	}
}

func TestOpenPullRequestsPages(t *testing.T) {
	server, client := newTestClient(t)
	for n := 1; n <= 150; n++ {
		server.SetPullRequest("acme/app", githubtest.PullRequest{Number: n, HeadSHA: sha1})
	}
	server.SetPullRequest("acme/app", githubtest.PullRequest{Number: 151, HeadSHA: sha1, State: "closed"})

	prs, err := client.OpenPullRequests(context.Background(), "acme/app")
	if err != nil {
		t.Fatalf("OpenPullRequests: %v", err)
	}
	if len(prs) != 150 {
		t.Errorf("%d open pull requests, want 150", len(prs))
	}
	if n := server.Requests(routeListPulls); n != 2 {
		t.Errorf("%d pages requested, want 2 of 100", n)
	}
}

func TestWorkflowSucceeded(t *testing.T) {
	server, client := newTestClient(t)
	server.AddRun("acme/app", githubtest.Run{File: "preview.yml", HeadSHA: sha1, Conclusion: "success"})
	server.AddRun("acme/app", githubtest.Run{File: "preview.yml", HeadSHA: sha2, Conclusion: "failure"})
	server.AddRun("acme/app", githubtest.Run{File: "lint.yml", HeadSHA: sha2, Conclusion: "success"})

	for _, tt := range []struct {
		file, sha string
		want      bool
	}{
		{"preview.yml", sha1, true},
		{"preview.yml", sha2, false}, // failed
		{"lint.yml", sha1, false},    // another workflow
		{"preview.yml", strings.Repeat("3", 40), false},
	} {
		got, err := client.WorkflowSucceeded(context.Background(), "acme/app", tt.file, tt.sha)
		if err != nil {
			t.Fatalf("WorkflowSucceeded(%s, %s): %v", tt.file, tt.sha, err)
		}
		if got != tt.want {
			t.Errorf("WorkflowSucceeded(%s, %s) = %v, want %v", tt.file, tt.sha[:7], got, tt.want)
		}
	}
}

func TestNotInstalled(t *testing.T) {
	_, client := newTestClient(t)
	if _, err := client.PullRequest(context.Background(), "acme/other", 1); err == nil {
		t.Error("a repository without the App gave no error")
	}
	if _, err := client.PullRequest(context.Background(), "acme", 1); err == nil {
		t.Error("a repository without owner gave no error")
	}
}

// TestTokenRenewed: a token is kept while it has more than tokenMargin
// left, and renewed after.
func TestTokenRenewed(t *testing.T) {
	for _, tt := range []struct {
		lifetime   time.Duration
		wantTokens int
	}{
		{time.Hour, 1},       // kept for the three requests
		{4 * time.Minute, 3}, // less than tokenMargin left: renewed each time
	} {
		server, client := newTestClient(t)
		server.TokenLifetime = tt.lifetime
		server.SetPullRequest("acme/app", githubtest.PullRequest{Number: 1, HeadSHA: sha1})
		for range 3 {
			if _, err := client.PullRequest(context.Background(), "acme/app", 1); err != nil {
				t.Fatal(err)
			}
		}
		if n := server.Requests(routeToken); n != tt.wantTokens {
			t.Errorf("tokens of %s: %d created, want %d", tt.lifetime, n, tt.wantTokens)
		}
	}
}

// TestWrongKey: GitHub refuses the JWT of a key that is not the App's.
func TestWrongKey(t *testing.T) {
	server := githubtest.New(t)
	server.AddRepo("acme/app", 7)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(other)})
	app, err := NewApp(server.AppID, otherPEM, server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(app).PullRequest(context.Background(), "acme/app", 1); err == nil {
		t.Error("a JWT signed with another key was accepted")
	}
}

func TestNewApp(t *testing.T) {
	server := githubtest.New(t)
	if _, err := NewApp(0, server.KeyPEM, "", "test"); err == nil {
		t.Error("App ID 0 accepted")
	}
	_, err := NewApp(1, []byte("-----BEGIN RSA PRIVATE KEY-----\nnot a key\n-----END RSA PRIVATE KEY-----\n"), "", "test")
	if err == nil {
		t.Fatal("an invalid key was accepted")
	}
	if strings.Contains(err.Error(), "not a key") {
		t.Errorf("the error quotes the key: %v", err)
	}
}

func TestParseAppID(t *testing.T) {
	if id, err := ParseAppID("123456\n"); err != nil || id != 123456 {
		t.Errorf("ParseAppID = %d, %v; want 123456", id, err)
	}
	for _, bad := range []string{"", "0", "-3", "abc", "12 34"} {
		if _, err := ParseAppID(bad); err == nil {
			t.Errorf("ParseAppID(%q) accepted", bad)
		}
	}
}

func TestDeployments(t *testing.T) {
	server, client := newTestClient(t)
	ctx := context.Background()

	if d, err := client.LatestDeployment(ctx, "acme/app", "acme-app-pr-1", sha1); err != nil || d.ID != 0 {
		t.Fatalf("LatestDeployment before any = %+v, %v; want none", d, err)
	}
	id, err := client.CreateDeployment(ctx, "acme/app", "acme-app-pr-1", sha1)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := client.LatestDeployment(ctx, "acme/app", "acme-app-pr-1", sha1); d.ID != id || d.State != "" {
		t.Errorf("LatestDeployment without status = %+v, want ID %d and no state", d, id)
	}
	if err := client.SetDeploymentStatus(ctx, "acme/app", id, "success", "https://p.example.com", "ready"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", 200)
	if err := client.SetDeploymentStatus(ctx, "acme/app", id, "inactive", "", long); err != nil {
		t.Fatalf("a long description was not cut to what GitHub accepts: %v", err)
	}
	d, err := client.LatestDeployment(ctx, "acme/app", "acme-app-pr-1", sha1)
	if err != nil || d.ID != id || d.State != "inactive" || len([]rune(d.Description)) != maxDescription {
		t.Errorf("LatestDeployment = %+v, %v; want the inactive status, cut to %d", d, err, maxDescription)
	}
	got := server.Deployments("acme/app")
	if len(got) != 1 || got[0].Statuses[0].EnvironmentURL != "https://p.example.com" {
		t.Errorf("deployments = %+v", got)
	}

	// Another environment, or another commit: not the same deployment.
	if d, _ := client.LatestDeployment(ctx, "acme/app", "acme-app-pr-2", sha1); d.ID != 0 {
		t.Errorf("deployment of another environment found: %+v", d)
	}
	if d, _ := client.LatestDeployment(ctx, "acme/app", "acme-app-pr-1", sha2); d.ID != 0 {
		t.Errorf("deployment of another commit found: %+v", d)
	}
}

func TestUpsertComment(t *testing.T) {
	server, client := newTestClient(t)
	ctx := context.Background()
	marker := Marker("acme-app-pr-5")

	// A person quotes the marker: the App does not take that comment.
	server.AddComment("acme/app", 5, "look: "+marker, 0)

	id, err := client.UpsertComment(ctx, "acme/app", 5, 0, marker, marker+"\nfirst")
	if err != nil {
		t.Fatal(err)
	}
	// Known ID: edited in place.
	if again, err := client.UpsertComment(ctx, "acme/app", 5, id, marker, marker+"\nsecond"); err != nil || again != id {
		t.Fatalf("UpsertComment with its ID = %d, %v; want %d", again, err, id)
	}
	// ID lost (the preview was deleted): found by its marker.
	if again, err := client.UpsertComment(ctx, "acme/app", 5, 0, marker, marker+"\nthird"); err != nil || again != id {
		t.Fatalf("UpsertComment by marker = %d, %v; want %d", again, err, id)
	}
	comments := server.Comments("acme/app", 5)
	if len(comments) != 2 || comments[0].Body != "look: "+marker || comments[1].Body != marker+"\nthird" {
		t.Errorf("comments = %+v, want the person's untouched and one of the App", comments)
	}

	// Its comment deleted by someone: a new one.
	server.DeleteComment("acme/app", id)
	newID, err := client.UpsertComment(ctx, "acme/app", 5, id, marker, marker+"\nfourth")
	if err != nil || newID == id {
		t.Fatalf("UpsertComment after deletion = %d, %v; want a new comment", newID, err)
	}
	if n := len(server.Comments("acme/app", 5)); n != 2 {
		t.Errorf("%d comments, want 2", n)
	}
}

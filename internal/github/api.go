// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"fmt"
	"strings"
	"sync"

	gh "github.com/google/go-github/v93/github"
)

// PullRequest is what Vesper needs to know of a pull request, read from the
// API rather than from a webhook payload: deliveries may come late, twice,
// or out of order, the API tells the current state (ADR-0007).
type PullRequest struct {
	Number int
	// Open is false once the pull request is closed or merged.
	Open bool
	// Draft pull requests get no preview until they are ready for review.
	Draft bool
	// HeadSHA is the head commit, in full.
	HeadSHA string
	// Fork is true when the head branch lives in another repository than the
	// base, or in a repository that no longer exists.
	Fork bool
	// Labels are the names of its labels.
	Labels []string
}

// HasLabel reports whether the pull request has the label name.
func (p PullRequest) HasLabel(name string) bool {
	for _, label := range p.Labels {
		if label == name {
			return true
		}
	}
	return false
}

// API is what the integration asks GitHub, for the repositories the App is
// installed on. The reconciler uses it through this interface, so that its
// tests can answer in its place.
type API interface {
	// PullRequest returns the pull request number of repository
	// (<owner>/<name>).
	PullRequest(ctx context.Context, repository string, number int) (PullRequest, error)
	// OpenPullRequests returns the open pull requests of repository.
	OpenPullRequests(ctx context.Context, repository string) ([]PullRequest, error)
	// WorkflowSucceeded reports whether a run of the workflow file succeeded
	// on the commit sha of repository.
	WorkflowSucceeded(ctx context.Context, repository, file, sha string) (bool, error)
}

// Client is the API, through the installations of the App.
type Client struct {
	app *App

	mu            sync.Mutex
	installations map[string]int64 // by repository, lowercase
}

// NewClient returns a Client that reaches GitHub as app.
func NewClient(app *App) *Client {
	return &Client{app: app, installations: map[string]int64{}}
}

var _ API = (*Client)(nil)

// repoClient returns a client of the installation of the App on repository,
// with its owner and name.
func (c *Client) repoClient(ctx context.Context, repository string) (*gh.Client, string, string, error) {
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || name == "" {
		return nil, "", "", fmt.Errorf("repository %q is not <owner>/<name>", repository)
	}
	key := strings.ToLower(repository)

	c.mu.Lock()
	id, known := c.installations[key]
	c.mu.Unlock()
	if !known {
		var err error
		if id, err = c.app.Installation(ctx, owner, name); err != nil {
			return nil, "", "", err
		}
		c.mu.Lock()
		c.installations[key] = id
		c.mu.Unlock()
	}

	client, err := c.app.Client(ctx, id)
	if err != nil {
		// The App may have been uninstalled and installed again, with a new
		// ID: look it up again next time.
		c.mu.Lock()
		delete(c.installations, key)
		c.mu.Unlock()
		return nil, "", "", err
	}
	return client, owner, name, nil
}

// PullRequest implements API.
func (c *Client) PullRequest(ctx context.Context, repository string, number int) (PullRequest, error) {
	client, owner, name, err := c.repoClient(ctx, repository)
	if err != nil {
		return PullRequest{}, err
	}
	pr, _, err := client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return PullRequest{}, fmt.Errorf("get pull request %s#%d: %w", repository, number, err)
	}
	return toPullRequest(pr), nil
}

// OpenPullRequests implements API, through every page.
func (c *Client) OpenPullRequests(ctx context.Context, repository string) ([]PullRequest, error) {
	client, owner, name, err := c.repoClient(ctx, repository)
	if err != nil {
		return nil, err
	}
	opts := &gh.PullRequestListOptions{State: "open", ListOptions: gh.ListOptions{PerPage: 100}}
	var all []PullRequest
	for {
		prs, resp, err := client.PullRequests.List(ctx, owner, name, opts)
		if err != nil {
			return nil, fmt.Errorf("list the open pull requests of %s: %w", repository, err)
		}
		for _, pr := range prs {
			all = append(all, toPullRequest(pr))
		}
		if resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
}

// WorkflowSucceeded implements API.
func (c *Client) WorkflowSucceeded(ctx context.Context, repository, file, sha string) (bool, error) {
	client, owner, name, err := c.repoClient(ctx, repository)
	if err != nil {
		return false, err
	}
	runs, _, err := client.Actions.ListWorkflowRunsByFileName(ctx, owner, name, file,
		&gh.ListWorkflowRunsOptions{HeadSHA: sha, Status: "success", ListOptions: gh.ListOptions{PerPage: 1}})
	if err != nil {
		return false, fmt.Errorf("list the runs of %s on %s at %s: %w", file, repository, sha, err)
	}
	return runs.GetTotalCount() > 0, nil
}

// toPullRequest keeps what Vesper needs of a pull request of the API.
func toPullRequest(pr *gh.PullRequest) PullRequest {
	head, base := pr.GetHead().GetRepo(), pr.GetBase().GetRepo()
	labels := make([]string, 0, len(pr.Labels))
	for _, label := range pr.Labels {
		labels = append(labels, label.GetName())
	}
	return PullRequest{
		Number:  pr.GetNumber(),
		Open:    pr.GetState() == "open",
		Draft:   pr.GetDraft(),
		HeadSHA: pr.GetHead().GetSHA(),
		Fork:    head == nil || !strings.EqualFold(head.GetFullName(), base.GetFullName()),
		Labels:  labels,
	}
}

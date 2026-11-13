// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

	// LatestDeployment returns the latest deployment of the commit sha to
	// environment, with its latest status; a zero Deployment if none.
	LatestDeployment(ctx context.Context, repository, environment, sha string) (Deployment, error)
	// CreateDeployment records that the commit sha is deployed to
	// environment, a transient one, and returns its ID.
	CreateDeployment(ctx context.Context, repository, environment, sha string) (int64, error)
	// SetDeploymentStatus adds a status to the deployment id: state is
	// in_progress, success, failure or inactive; url is the address of the
	// preview, if any.
	SetDeploymentStatus(ctx context.Context, repository string, id int64, state, url, description string) error
	// UpsertComment writes body as the comment of the App on the pull
	// request number that holds marker: it edits commentID if it is still
	// there, or the comment of the App with marker, or creates one. It
	// returns the ID of the comment.
	UpsertComment(ctx context.Context, repository string, number int, commentID int64, marker, body string) (int64, error)
}

// Deployment is the latest deployment of a commit to an environment.
type Deployment struct {
	// ID is 0 when there is none.
	ID int64
	// State and Description are those of its latest status; empty when it
	// has none.
	State       string
	Description string
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

// IsNotFound reports whether err is a 404 of the GitHub API: a pull request
// or a repository that does not exist, or that the App no longer reaches.
func IsNotFound(err error) bool {
	var response *gh.ErrorResponse
	return errors.As(err, &response) && response.Response != nil && response.Response.StatusCode == http.StatusNotFound
}

// maxDescription is the longest description of a deployment status GitHub
// accepts.
const maxDescription = 140

// truncate cuts s to n runes, with an ellipsis if it was longer.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// LatestDeployment implements API.
func (c *Client) LatestDeployment(ctx context.Context, repository, environment, sha string) (Deployment, error) {
	client, owner, name, err := c.repoClient(ctx, repository)
	if err != nil {
		return Deployment{}, err
	}
	deployments, _, err := client.Repositories.ListDeployments(ctx, owner, name,
		&gh.DeploymentsListOptions{SHA: sha, Environment: environment, ListOptions: gh.ListOptions{PerPage: 1}})
	if err != nil {
		return Deployment{}, fmt.Errorf("list the deployments of %s to %s: %w", repository, environment, err)
	}
	if len(deployments) == 0 {
		return Deployment{}, nil
	}
	d := Deployment{ID: deployments[0].GetID()}
	statuses, _, err := client.Repositories.ListDeploymentStatuses(ctx, owner, name, d.ID, &gh.ListOptions{PerPage: 1})
	if err != nil {
		return Deployment{}, fmt.Errorf("list the statuses of deployment %d of %s: %w", d.ID, repository, err)
	}
	if len(statuses) > 0 {
		d.State, d.Description = statuses[0].GetState(), statuses[0].GetDescription()
	}
	return d, nil
}

// CreateDeployment implements API. The deployment needs no status checks
// and merges nothing: it only records what the preview runs.
func (c *Client) CreateDeployment(ctx context.Context, repository, environment, sha string) (int64, error) {
	client, owner, name, err := c.repoClient(ctx, repository)
	if err != nil {
		return 0, err
	}
	deployment, _, err := client.Repositories.CreateDeployment(ctx, owner, name, gh.DeploymentRequest{
		Ref:                   sha,
		Environment:           new(environment),
		Description:           new("Preview environment, by Vesper"),
		AutoMerge:             new(false),
		RequiredContexts:      []string{},
		TransientEnvironment:  new(true),
		ProductionEnvironment: new(false),
	})
	if err != nil {
		return 0, fmt.Errorf("create a deployment of %s to %s: %w", repository, environment, err)
	}
	return deployment.GetID(), nil
}

// SetDeploymentStatus implements API. A successful status makes the earlier
// deployments of the environment inactive.
func (c *Client) SetDeploymentStatus(ctx context.Context, repository string, id int64, state, url, description string) error {
	client, owner, name, err := c.repoClient(ctx, repository)
	if err != nil {
		return err
	}
	request := gh.DeploymentStatusRequest{
		State:        state,
		Description:  new(truncate(description, maxDescription)),
		AutoInactive: new(true),
	}
	if url != "" {
		request.EnvironmentURL = new(url)
	}
	if _, _, err := client.Repositories.CreateDeploymentStatus(ctx, owner, name, id, request); err != nil {
		return fmt.Errorf("set the status of deployment %d of %s to %s: %w", id, repository, state, err)
	}
	return nil
}

// UpsertComment implements API. A comment is the App's only if GitHub says
// the App wrote it: a person who copies the marker into a comment does not
// make the App edit it.
func (c *Client) UpsertComment(ctx context.Context, repository string, number int, commentID int64,
	marker, body string) (int64, error) {
	client, owner, name, err := c.repoClient(ctx, repository)
	if err != nil {
		return 0, err
	}
	request := gh.IssueCommentRequest{Body: body}
	if commentID > 0 {
		comment, _, err := client.Issues.UpdateComment(ctx, owner, name, commentID, request)
		if err == nil {
			return comment.GetID(), nil
		}
		if !IsNotFound(err) {
			return 0, fmt.Errorf("edit comment %d of %s#%d: %w", commentID, repository, number, err)
		}
		// Deleted by someone: look for another, or write a new one.
	}

	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, name, number, opts)
		if err != nil {
			return 0, fmt.Errorf("list the comments of %s#%d: %w", repository, number, err)
		}
		for _, comment := range comments {
			if comment.GetPerformedViaGithubApp().GetID() == c.app.id && strings.Contains(comment.GetBody(), marker) {
				edited, _, err := client.Issues.UpdateComment(ctx, owner, name, comment.GetID(), request)
				if err != nil {
					return 0, fmt.Errorf("edit comment %d of %s#%d: %w", comment.GetID(), repository, number, err)
				}
				return edited.GetID(), nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	comment, _, err := client.Issues.CreateComment(ctx, owner, name, number, request)
	if err != nil {
		return 0, fmt.Errorf("comment on %s#%d: %w", repository, number, err)
	}
	return comment.GetID(), nil
}

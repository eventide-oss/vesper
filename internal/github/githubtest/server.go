// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

// Package githubtest is a fake of the GitHub API, for the tests of the
// integration: the few endpoints Vesper calls, with the authentication of a
// GitHub App. It checks the JWT of the App against its public key, issues
// installation tokens, and serves a repository only to a token of the
// installation it belongs to.
package githubtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// PullRequest is a pull request of a fake repository.
type PullRequest struct {
	Number  int
	State   string // open or closed
	Draft   bool
	HeadSHA string
	// HeadRepo is the repository of the head branch: empty for the
	// repository itself, the name of a fork, or "-" for a deleted fork.
	HeadRepo string
	Labels   []string
}

// Run is a run of a workflow of a fake repository.
type Run struct {
	File       string
	HeadSHA    string
	Conclusion string // success, failure...
}

// Deployment is a deployment of a fake repository, with its statuses,
// newest last.
type Deployment struct {
	ID          int64
	SHA         string
	Environment string
	Statuses    []DeploymentStatus
}

// DeploymentStatus is a status of a fake deployment.
type DeploymentStatus struct {
	State          string
	Description    string
	EnvironmentURL string
}

// Comment is a comment on a pull request of a fake repository.
type Comment struct {
	ID     int64
	Number int
	Body   string
	// AppID is the App that wrote it; 0 for a person.
	AppID int64
}

type repo struct {
	installation int64
	pulls        map[int]PullRequest
	runs         []Run
	deployments  []*Deployment
	comments     []*Comment
}

// Server is a fake GitHub API, started for a test and closed at its end.
type Server struct {
	*httptest.Server
	// AppID and KeyPEM are the ID and the private key of the App it knows.
	AppID  int64
	KeyPEM []byte
	// TokenLifetime is how long the installation tokens it issues last.
	TokenLifetime time.Duration

	key *rsa.PrivateKey

	mu       sync.Mutex
	repos    map[string]*repo // by full name, lowercase
	tokens   map[string]int64 // installation token -> installation ID
	issued   int
	nextID   int64
	requests map[string]int // by route
}

// New starts a fake GitHub for an App with a new key.
func New(t *testing.T) *Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		AppID:         12345,
		KeyPEM:        pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		TokenLifetime: time.Hour,
		key:           key,
		repos:         map[string]*repo{},
		tokens:        map[string]int64{},
		requests:      map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", s.createToken)
	mux.HandleFunc("GET /repos/{owner}/{name}/installation", s.installation)
	mux.HandleFunc("GET /repos/{owner}/{name}/pulls", s.listPulls)
	mux.HandleFunc("GET /repos/{owner}/{name}/pulls/{number}", s.getPull)
	mux.HandleFunc("GET /repos/{owner}/{name}/actions/workflows/{file}/runs", s.listRuns)
	mux.HandleFunc("GET /repos/{owner}/{name}/deployments", s.listDeployments)
	mux.HandleFunc("POST /repos/{owner}/{name}/deployments", s.createDeployment)
	mux.HandleFunc("GET /repos/{owner}/{name}/deployments/{id}/statuses", s.listStatuses)
	mux.HandleFunc("POST /repos/{owner}/{name}/deployments/{id}/statuses", s.createStatus)
	mux.HandleFunc("GET /repos/{owner}/{name}/issues/{number}/comments", s.listComments)
	mux.HandleFunc("POST /repos/{owner}/{name}/issues/{number}/comments", s.createComment)
	mux.HandleFunc("PATCH /repos/{owner}/{name}/issues/comments/{id}", s.updateComment)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// AddRepo makes the App installed on repository (<owner>/<name>), in the
// installation installationID.
func (s *Server) AddRepo(repository string, installationID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[strings.ToLower(repository)] = &repo{installation: installationID, pulls: map[int]PullRequest{}}
}

// SetPullRequest creates or replaces a pull request of repository. An empty
// HeadRepo means the repository itself, an empty State open.
func (s *Server) SetPullRequest(repository string, pr PullRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pr.State == "" {
		pr.State = "open"
	}
	if pr.HeadRepo == "" {
		pr.HeadRepo = repository
	}
	s.repos[strings.ToLower(repository)].pulls[pr.Number] = pr
}

// AddRun records a run of a workflow of repository.
func (s *Server) AddRun(repository string, run Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[strings.ToLower(repository)]
	r.runs = append(r.runs, run)
}

// Requests returns how many requests reached route, such as
// "POST /app/installations/{id}/access_tokens".
func (s *Server) Requests(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[route]
}

func (s *Server) count(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[r.Pattern]++
}

// appAuthenticated checks the JWT of the App, as GitHub does.
func (s *Server) appAuthenticated(w http.ResponseWriter, r *http.Request) bool {
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		writeError(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return false
	}
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(bearer, claims, func(*jwt.Token) (any, error) { return &s.key.PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || claims.Issuer != strconv.FormatInt(s.AppID, 10) {
		writeError(w, http.StatusUnauthorized, "'Expiration time' claim ('exp') is too far in the future")
		return false
	}
	if time.Until(claims.ExpiresAt.Time) > 10*time.Minute {
		writeError(w, http.StatusUnauthorized, "'Expiration time' claim ('exp') is too far in the future")
		return false
	}
	return true
}

// repoAuthenticated returns the repository of the request, if its token
// belongs to the installation of that repository.
func (s *Server) repoAuthenticated(w http.ResponseWriter, r *http.Request) (*repo, bool) {
	full := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("name"))
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, known := s.repos[full]
	installation, valid := s.tokens[bearer]
	if !valid {
		writeError(w, http.StatusUnauthorized, "Bad credentials")
		return nil, false
	}
	if !known || rp.installation != installation {
		writeError(w, http.StatusNotFound, "Not Found")
		return nil, false
	}
	return rp, true
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	if !s.appAuthenticated(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	s.issued++
	value := fmt.Sprintf("ghs_fake_%d_%d", id, s.issued)
	s.tokens[value] = id
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      value,
		"expires_at": time.Now().Add(s.TokenLifetime).UTC().Format(time.RFC3339),
	})
}

func (s *Server) installation(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	if !s.appAuthenticated(w, r) {
		return
	}
	s.mu.Lock()
	rp, ok := s.repos[strings.ToLower(r.PathValue("owner")+"/"+r.PathValue("name"))]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rp.installation})
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	number, _ := strconv.Atoi(r.PathValue("number"))
	s.mu.Lock()
	pr, found := rp.pulls[number]
	s.mu.Unlock()
	if !found {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, pullJSON(r, pr))
}

func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	var open []PullRequest
	for _, pr := range rp.pulls {
		if pr.State == "open" {
			open = append(open, pr)
		}
	}
	s.mu.Unlock()
	slices.SortFunc(open, func(a, b PullRequest) int { return b.Number - a.Number })

	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage <= 0 {
		perPage = 30
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	start := min((page-1)*perPage, len(open))
	end := min(start+perPage, len(open))
	if end < len(open) {
		next := *r.URL
		q := next.Query()
		q.Set("page", strconv.Itoa(page+1))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, "http://"+r.Host, next.RequestURI()))
	}
	items := make([]any, 0, end-start)
	for _, pr := range open[start:end] {
		items = append(items, pullJSON(r, pr))
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	s.mu.Lock()
	var matching []any
	for i, run := range rp.runs {
		if run.File == r.PathValue("file") && (q.Get("head_sha") == "" || run.HeadSHA == q.Get("head_sha")) &&
			(q.Get("status") == "" || run.Conclusion == q.Get("status")) {
			matching = append(matching, map[string]any{
				"id": i + 1, "head_sha": run.HeadSHA, "status": "completed", "conclusion": run.Conclusion,
			})
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(matching), "workflow_runs": matching})
}

// Deployments returns the deployments of repository, oldest first.
func (s *Server) Deployments(repository string) []Deployment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Deployment
	for _, d := range s.repos[strings.ToLower(repository)].deployments {
		c := *d
		c.Statuses = append([]DeploymentStatus(nil), d.Statuses...)
		out = append(out, c)
	}
	return out
}

// Comments returns the comments of the pull request number of repository.
func (s *Server) Comments(repository string, number int) []Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Comment
	for _, c := range s.repos[strings.ToLower(repository)].comments {
		if c.Number == number {
			out = append(out, *c)
		}
	}
	return out
}

// AddComment adds a comment to a pull request of repository, as a person
// (appID 0) or an App; it returns its ID.
func (s *Server) AddComment(repository string, number int, body string, appID int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	r := s.repos[strings.ToLower(repository)]
	r.comments = append(r.comments, &Comment{ID: s.nextID, Number: number, Body: body, AppID: appID})
	return s.nextID
}

// DeleteComment deletes the comment id of repository, as a person may.
func (s *Server) DeleteComment(repository string, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[strings.ToLower(repository)]
	r.comments = slices.DeleteFunc(r.comments, func(c *Comment) bool { return c.ID == id })
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	s.mu.Lock()
	items := []any{}
	for i := len(rp.deployments) - 1; i >= 0; i-- { // newest first, as GitHub
		d := rp.deployments[i]
		if (q.Get("sha") == "" || d.SHA == q.Get("sha")) && (q.Get("environment") == "" || d.Environment == q.Get("environment")) {
			items = append(items, map[string]any{"id": d.ID, "sha": d.SHA, "environment": d.Environment})
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	var body struct {
		Ref              string   `json:"ref"`
		Environment      string   `json:"environment"`
		RequiredContexts []string `json:"required_contexts"`
		Transient        bool     `json:"transient_environment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Ref == "" || body.RequiredContexts == nil || !body.Transient {
		writeError(w, http.StatusUnprocessableEntity, "a deployment needs a ref, required_contexts and transient_environment")
		return
	}
	s.mu.Lock()
	s.nextID++
	d := &Deployment{ID: s.nextID, SHA: body.Ref, Environment: body.Environment}
	rp.deployments = append(rp.deployments, d)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"id": d.ID, "sha": d.SHA, "environment": d.Environment})
}

func (s *Server) deployment(rp *repo, r *http.Request) *Deployment {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	for _, d := range rp.deployments {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (s *Server) listStatuses(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deployment(rp, r)
	if d == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	items := []any{}
	for i := len(d.Statuses) - 1; i >= 0; i-- { // newest first, as GitHub
		items = append(items, map[string]any{"state": d.Statuses[i].State, "description": d.Statuses[i].Description})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createStatus(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	var body struct {
		State          string `json:"state"`
		Description    string `json:"description"`
		EnvironmentURL string `json:"environment_url"`
		AutoInactive   *bool  `json:"auto_inactive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len([]rune(body.Description)) > 140 {
		writeError(w, http.StatusUnprocessableEntity, "invalid status, or a description over 140 characters")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deployment(rp, r)
	if d == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	d.Statuses = append(d.Statuses, DeploymentStatus{State: body.State, Description: body.Description,
		EnvironmentURL: body.EnvironmentURL})
	// As GitHub: a success makes the other deployments of the environment
	// inactive, unless auto_inactive is false.
	if body.State == "success" && (body.AutoInactive == nil || *body.AutoInactive) {
		for _, other := range rp.deployments {
			if other != d && other.Environment == d.Environment && len(other.Statuses) > 0 &&
				other.Statuses[len(other.Statuses)-1].State != "inactive" {
				other.Statuses = append(other.Statuses, DeploymentStatus{State: "inactive"})
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"state": body.State})
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	number, _ := strconv.Atoi(r.PathValue("number"))
	s.mu.Lock()
	items := []any{}
	for _, c := range rp.comments {
		if c.Number == number {
			items = append(items, commentJSON(c))
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		writeError(w, http.StatusUnprocessableEntity, "a comment needs a body")
		return
	}
	number, _ := strconv.Atoi(r.PathValue("number"))
	s.mu.Lock()
	s.nextID++
	c := &Comment{ID: s.nextID, Number: number, Body: body.Body, AppID: s.AppID}
	rp.comments = append(rp.comments, c)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, commentJSON(c))
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request) {
	s.count(r)
	rp, ok := s.repoAuthenticated(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		writeError(w, http.StatusUnprocessableEntity, "a comment needs a body")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range rp.comments {
		if c.ID == id {
			if c.AppID != s.AppID {
				// An App can edit only its own comments.
				writeError(w, http.StatusForbidden, "Resource not accessible by integration")
				return
			}
			c.Body = body.Body
			writeJSON(w, http.StatusOK, commentJSON(c))
			return
		}
	}
	writeError(w, http.StatusNotFound, "Not Found")
}

func commentJSON(c *Comment) map[string]any {
	out := map[string]any{"id": c.ID, "body": c.Body, "performed_via_github_app": nil}
	if c.AppID != 0 {
		out["performed_via_github_app"] = map[string]any{"id": c.AppID}
	}
	return out
}

func pullJSON(r *http.Request, pr PullRequest) map[string]any {
	base := r.PathValue("owner") + "/" + r.PathValue("name")
	labels := make([]any, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	head := map[string]any{"sha": pr.HeadSHA, "repo": nil}
	if pr.HeadRepo != "-" {
		head["repo"] = map[string]any{"full_name": pr.HeadRepo}
	}
	return map[string]any{
		"number": pr.Number,
		"state":  pr.State,
		"draft":  pr.Draft,
		"labels": labels,
		"head":   head,
		"base":   map[string]any{"repo": map[string]any{"full_name": base}},
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"message": message})
}

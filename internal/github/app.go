// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	gh "github.com/google/go-github/v93/github"
)

// The lifetimes of the credentials of the App. GitHub accepts an App JWT for
// 10 minutes at most, and issues installation tokens for one hour.
const (
	// jwtLifetime is how long an App JWT is valid; jwtBackdate covers a clock
	// of GitHub slightly behind ours.
	jwtLifetime = 9 * time.Minute
	jwtBackdate = time.Minute
	// tokenMargin is how long before its expiry an installation token is
	// renewed, so that no request goes out with a token about to expire.
	tokenMargin = 5 * time.Minute
	// apiTimeout bounds each request to the GitHub API.
	apiTimeout = 30 * time.Second
)

// App is the GitHub App of Vesper (ADR-0007). It signs JWTs with the private
// key of the App, exchanges them for installation tokens of one hour, and
// keeps each token until shortly before it expires.
type App struct {
	id        int64
	key       *rsa.PrivateKey
	baseURL   string // empty for api.github.com
	userAgent string
	now       func() time.Time

	mu     sync.Mutex
	tokens map[int64]token // by installation ID
}

type token struct {
	value     string
	expiresAt time.Time
}

// NewApp returns the App with the ID id and the private key in PEM, as
// GitHub gives it when the key is generated. baseURL is the URL of the API,
// empty for api.github.com; tests set it to a fake GitHub.
func NewApp(id int64, keyPEM []byte, baseURL, userAgent string) (*App, error) {
	if id <= 0 {
		return nil, errors.New("the App ID must be a positive number")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(keyPEM)
	if err != nil {
		// The error of the parser never quotes the key.
		return nil, fmt.Errorf("read the private key of the App: %w", err)
	}
	return &App{
		id:        id,
		key:       key,
		baseURL:   baseURL,
		userAgent: userAgent,
		now:       time.Now,
		tokens:    map[int64]token{},
	}, nil
}

// ParseAppID reads the ID of the App, as stored in its Secret.
func ParseAppID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("the App ID %q is not a positive number", strings.TrimSpace(s))
	}
	return id, nil
}

// signJWT returns a JWT that authenticates as the App itself, for the few
// requests that need it: finding an installation, creating its token.
func (a *App) signJWT() (string, error) {
	now := a.now()
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(a.id, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(a.key)
}

// newClient returns a client of the GitHub API that authenticates with
// bearer.
func (a *App) newClient(bearer string) (*gh.Client, error) {
	opts := []gh.ClientOptionsFunc{
		gh.WithHTTPClient(&http.Client{Timeout: apiTimeout}),
		gh.WithAuthToken(bearer),
		gh.WithUserAgent(a.userAgent),
	}
	if a.baseURL != "" {
		base := strings.TrimSuffix(a.baseURL, "/") + "/"
		opts = append(opts, gh.WithURLs(&base, &base))
	}
	return gh.NewClient(opts...)
}

// appClient returns a client that authenticates as the App.
func (a *App) appClient() (*gh.Client, error) {
	signed, err := a.signJWT()
	if err != nil {
		return nil, fmt.Errorf("sign the JWT of the App: %w", err)
	}
	return a.newClient(signed)
}

// Installation returns the ID of the installation of the App on the
// repository owner/name: an error if the App is not installed there.
func (a *App) Installation(ctx context.Context, owner, name string) (int64, error) {
	client, err := a.appClient()
	if err != nil {
		return 0, err
	}
	installation, _, err := client.Apps.GetRepositoryInstallation(ctx, owner, name)
	if err != nil {
		return 0, fmt.Errorf("find the installation of the App on %s/%s: %w", owner, name, err)
	}
	return installation.GetID(), nil
}

// Client returns a client of the GitHub API that authenticates as the
// installation installationID: it reaches the repositories of that
// installation, with the permissions of the App.
func (a *App) Client(ctx context.Context, installationID int64) (*gh.Client, error) {
	value, err := a.installationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return a.newClient(value)
}

// installationToken returns a token of the installation, from the cache
// while it has more than tokenMargin left, or new from GitHub.
func (a *App) installationToken(ctx context.Context, installationID int64) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[installationID]; ok && a.now().Add(tokenMargin).Before(t.expiresAt) {
		return t.value, nil
	}

	client, err := a.appClient()
	if err != nil {
		return "", err
	}
	created, _, err := client.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return "", fmt.Errorf("create a token for installation %d: %w", installationID, err)
	}
	if created.GetToken() == "" {
		return "", fmt.Errorf("GitHub returned no token for installation %d", installationID)
	}
	t := token{value: created.GetToken(), expiresAt: created.GetExpiresAt().Time}
	a.tokens[installationID] = t
	return t.value, nil
}

// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// WebhookPath is the path of the webhook endpoint, set as the webhook URL of
// the GitHub App: https://<host>/webhook.
const WebhookPath = "/webhook"

// The limits of the webhook server. It faces the Internet through the ingress
// controller: a slow or endless request must not hold it.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 64 << 10
	shutdownTimeout   = 10 * time.Second
)

// Server serves the webhook endpoint. It is a Runnable of the manager of
// vesper-github: it starts with it and stops when its context is done.
type Server struct {
	server *http.Server
	listen func() (net.Listener, error)
}

// NewServer returns a Server that listens on addr and serves handler on
// WebhookPath only. TLS is terminated by the ingress controller.
func NewServer(addr string, handler http.Handler) *Server {
	mux := http.NewServeMux()
	mux.Handle(WebhookPath, handler)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	return &Server{
		server: server,
		listen: func() (net.Listener, error) { return net.Listen("tcp", addr) },
	}
}

// Start serves until ctx is done, then lets the requests in flight finish.
func (s *Server) Start(ctx context.Context) error {
	listener, err := s.listen()
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- s.server.Serve(listener) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// NeedLeaderElection is false: every replica answers webhooks.
func (s *Server) NeedLeaderElection() bool { return false }

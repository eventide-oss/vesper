// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestServer serves the webhook path only, with timeouts, and stops when its
// context is done.
func TestServer(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	s := NewServer("127.0.0.1:0", handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.listen = func() (net.Listener, error) { return listener, nil }

	if s.server.ReadHeaderTimeout == 0 || s.server.ReadTimeout == 0 || s.server.WriteTimeout == 0 ||
		s.server.IdleTimeout == 0 || s.server.MaxHeaderBytes == 0 {
		t.Errorf("server without limits: %+v", s.server)
	}
	if s.NeedLeaderElection() {
		t.Error("NeedLeaderElection = true, want every replica to serve")
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- s.Start(ctx) }()

	// Without keep-alive: an idle connection the client opened ahead would
	// hold Shutdown for 5 seconds, as a new connection.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	base := "http://" + listener.Addr().String()
	for path, want := range map[string]int{WebhookPath: http.StatusTeapot, "/": http.StatusNotFound, "/metrics": http.StatusNotFound} {
		resp, err := client.Post(base+path, "application/json", http.NoBody) //nolint:noctx // a test
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("POST %s = %d, want %d", path, resp.StatusCode, want)
		}
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Start = %v, want nil after the context is done", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
}

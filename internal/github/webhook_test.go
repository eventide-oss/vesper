// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const testSecret = "It's a Secret to Everybody"

// sign returns the X-Hub-Signature-256 header GitHub sends for body.
func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// delivery builds a request as GitHub sends it, signed with testSecret.
func delivery(event, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerEvent, event)
	req.Header.Set(headerDelivery, "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	req.Header.Set(headerSignature, sign(testSecret, body))
	return req
}

// newTestReceiver returns a Receiver with testSecret, its registry, and the
// lines it logs, at every level.
func newTestReceiver(t *testing.T, secret func() ([]byte, error)) (*Receiver, *prometheus.Registry, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	log := funcr.New(func(prefix, args string) { fmt.Fprintln(&logs, prefix, args) }, funcr.Options{Verbosity: 10})
	reg := prometheus.NewRegistry()
	if secret == nil {
		secret = func() ([]byte, error) { return []byte(testSecret), nil }
	}
	r, err := NewReceiver(secret, log, reg)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return r, reg, &logs
}

func TestReceiver(t *testing.T) {
	const payload = `{"action":"opened","number":42,"pull_request":{"head":{"ref":"feature"}}}`

	tests := []struct {
		name       string
		request    func() *http.Request
		wantStatus int
		wantResult string
	}{
		{"ping", func() *http.Request { return delivery("ping", `{"zen":"Keep it logically awesome."}`) },
			http.StatusOK, resultAccepted},
		{"pull_request", func() *http.Request { return delivery("pull_request", payload) },
			http.StatusAccepted, resultAccepted},
		{"workflow_run", func() *http.Request { return delivery("workflow_run", `{"action":"completed"}`) },
			http.StatusAccepted, resultAccepted},
		{"an event Vesper does not handle, signed", func() *http.Request { return delivery("installation", `{}`) },
			http.StatusOK, resultIgnored},
		{"no signature", func() *http.Request {
			req := delivery("pull_request", payload)
			req.Header.Del(headerSignature)
			return req
		}, http.StatusUnauthorized, resultInvalidSignature},
		{"signed with another secret", func() *http.Request {
			req := delivery("pull_request", payload)
			req.Header.Set(headerSignature, sign("another secret", payload))
			return req
		}, http.StatusUnauthorized, resultInvalidSignature},
		{"payload changed after signing", func() *http.Request {
			req := delivery("pull_request", strings.Replace(payload, "42", "43", 1))
			req.Header.Set(headerSignature, sign(testSecret, payload))
			return req
		}, http.StatusUnauthorized, resultInvalidSignature},
		{"the old SHA-1 signature only", func() *http.Request {
			req := delivery("pull_request", payload)
			req.Header.Del(headerSignature)
			req.Header.Set("X-Hub-Signature", "sha1=0123456789abcdef0123456789abcdef01234567")
			return req
		}, http.StatusUnauthorized, resultInvalidSignature},
		{"signature that is not hexadecimal", func() *http.Request {
			req := delivery("pull_request", payload)
			req.Header.Set(headerSignature, "sha256=not-hex")
			return req
		}, http.StatusUnauthorized, resultInvalidSignature},
		{"signature too short", func() *http.Request {
			req := delivery("pull_request", payload)
			req.Header.Set(headerSignature, "sha256=abcd")
			return req
		}, http.StatusUnauthorized, resultInvalidSignature},
		{"too large", func() *http.Request {
			return delivery("pull_request", `{"x":"`+strings.Repeat("a", MaxPayloadBytes)+`"}`)
		}, http.StatusRequestEntityTooLarge, resultTooLarge},
		{"GET", func() *http.Request {
			req := delivery("ping", `{}`)
			req.Method = http.MethodGet
			return req
		}, http.StatusMethodNotAllowed, resultMethodNotAllowed},
		{"form-encoded", func() *http.Request {
			req := delivery("ping", `payload=%7B%7D`)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return req
		}, http.StatusUnsupportedMediaType, resultUnsupportedMediaType},
		{"JSON with a charset", func() *http.Request {
			req := delivery("ping", `{}`)
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
			return req
		}, http.StatusOK, resultAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _ := newTestReceiver(t, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, tt.request())

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := testutil.ToFloat64(r.results.WithLabelValues(tt.wantResult)); got != 1 {
				t.Errorf("deliveries{result=%q} = %v, want 1", tt.wantResult, got)
			}
			if got := testutil.CollectAndCount(r.results); got != 1 {
				t.Errorf("%d results counted, want 1", got)
			}
		})
	}
}

// TestReceiverSecretError: without its secret, the receiver accepts
// nothing, and says why without saying more.
func TestReceiverSecretError(t *testing.T) {
	r, _, logs := newTestReceiver(t, func() ([]byte, error) {
		return nil, errors.New("open /etc/vesper-github/webhook-secret: no such file or directory")
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, delivery("ping", `{}`))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "cannot read the webhook secret") {
		t.Errorf("logs = %q, want the reason", logs)
	}
}

// TestReceiverLogsNoSecret: neither the secret, nor the signature, nor the
// payload reach the logs, at any level, whatever the result.
func TestReceiverLogsNoSecret(t *testing.T) {
	const payload = `{"pull_request":{"title":"private-title-0123"}}`
	r, _, logs := newTestReceiver(t, nil)
	for _, req := range []*http.Request{
		delivery("pull_request", payload),
		delivery("installation", payload),
		func() *http.Request {
			req := delivery("pull_request", payload)
			req.Header.Set(headerSignature, sign("wrong", payload))
			return req
		}(),
	} {
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	for _, leaked := range []string{testSecret, sign(testSecret, payload), "private-title-0123"} {
		if strings.Contains(logs.String(), leaked) {
			t.Errorf("logs contain %q:\n%s", leaked, logs)
		}
	}
}

// TestReceiverLogsUntrustedHeaders: the delivery ID and the event name are
// not signed; odd values are not logged as they are.
func TestReceiverLogsUntrustedHeaders(t *testing.T) {
	r, _, logs := newTestReceiver(t, nil)
	req := delivery("odd\nevent", `{}`)
	req.Header.Set(headerDelivery, "x\n\"injected\"")
	r.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(logs.String(), "injected") || strings.Contains(logs.String(), "odd\n") {
		t.Errorf("logs contain an untrusted header as is:\n%s", logs)
	}
	if !strings.Contains(logs.String(), `"delivery"="invalid"`) {
		t.Errorf("logs = %q, want the delivery marked invalid", logs)
	}
}

func TestSecretFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	got, err := SecretFile(write("with-newline", testSecret+"\n"))()
	if err != nil || string(got) != testSecret {
		t.Errorf("SecretFile = %q, %v; want the secret without its newline", got, err)
	}
	if _, err := SecretFile(write("empty", "\n"))(); err == nil {
		t.Error("an empty secret was accepted")
	}
	if _, err := SecretFile(filepath.Join(dir, "missing"))(); err == nil {
		t.Error("a missing file gave no error")
	}
}

func TestNewReceiverRegistersOnce(t *testing.T) {
	reg := prometheus.NewRegistry()
	secret := func() ([]byte, error) { return []byte(testSecret), nil }
	if _, err := NewReceiver(secret, logr.Discard(), reg); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := NewReceiver(secret, logr.Discard(), reg); err == nil {
		t.Error("a second receiver registered the same metric")
	}
}

func TestSecretCheck(t *testing.T) {
	if err := SecretCheck(func() ([]byte, error) { return []byte(testSecret), nil })(nil); err != nil {
		t.Errorf("ready with a secret: %v", err)
	}
	if err := SecretCheck(func() ([]byte, error) { return nil, errors.New("missing") })(nil); err == nil {
		t.Error("ready without a secret")
	}
}

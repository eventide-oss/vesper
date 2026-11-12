// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

// Package github receives the webhooks of the GitHub App of Vesper
// (ADR-0007). It is the only code of Vesper that reads data from the
// Internet: it authenticates every delivery before it looks at it.
package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
)

// MaxPayloadBytes is the largest delivery accepted. GitHub caps payloads at
// 25 MB, but the events Vesper handles are a few kilobytes: a larger body is
// refused before it is read in full.
const MaxPayloadBytes = 5 << 20

// The headers of a delivery, set by GitHub.
const (
	headerSignature = "X-Hub-Signature-256"
	headerEvent     = "X-GitHub-Event"
	headerDelivery  = "X-GitHub-Delivery"
	signaturePrefix = "sha256="
)

// The results of a delivery, as counted in the metrics.
const (
	resultAccepted             = "accepted"
	resultIgnored              = "ignored"
	resultInvalidSignature     = "invalid_signature"
	resultTooLarge             = "too_large"
	resultMethodNotAllowed     = "method_not_allowed"
	resultUnsupportedMediaType = "unsupported_media_type"
	resultBadRequest           = "bad_request"
	resultError                = "error"
)

// handledEvents are the events Vesper subscribes to; it accepts them. A
// signed delivery of any other event is acknowledged and ignored: GitHub
// sends some events, such as installation, to every App.
var handledEvents = map[string]bool{
	"ping":         true,
	"pull_request": true,
	"workflow_run": true,
}

// deliveryID matches the identifier GitHub gives each delivery. The header
// is not covered by the signature: anything else is not logged as is.
var deliveryID = regexp.MustCompile(`^[0-9a-fA-F-]{1,64}$`)

// Receiver is the HTTP handler of the webhook endpoint.
type Receiver struct {
	secret  func() ([]byte, error)
	log     logr.Logger
	results *prometheus.CounterVec
}

// NewReceiver returns a Receiver that checks deliveries with the webhook
// secret that secret returns, and counts them in reg.
func NewReceiver(secret func() ([]byte, error), log logr.Logger, reg prometheus.Registerer) (*Receiver, error) {
	results := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vesper_github_webhook_deliveries_total",
		Help: "Webhook deliveries received from GitHub, by result: accepted, ignored, or why they were refused.",
	}, []string{"result"})
	if err := reg.Register(results); err != nil {
		return nil, err
	}
	return &Receiver{secret: secret, log: log, results: results}, nil
}

// SecretFile returns a function that reads the webhook secret from path, a
// file of a Secret mounted in the pod: the kubelet updates it when the
// Secret changes, so it is read for each delivery. A trailing newline,
// often added by hand, is not part of the secret.
func SecretFile(path string) func() ([]byte, error) {
	return func() ([]byte, error) {
		secret, err := os.ReadFile(path) //nolint:gosec // the path is a flag set by the administrator
		if err != nil {
			return nil, err
		}
		secret = bytes.TrimRight(secret, "\r\n")
		if len(secret) == 0 {
			return nil, errors.New("the webhook secret is empty")
		}
		return secret, nil
	}
}

// ServeHTTP authenticates a delivery and answers GitHub. The body is read
// with a limit, then checked against its signature before anything else
// reads it.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	id := req.Header.Get(headerDelivery)
	if !deliveryID.MatchString(id) {
		id = "invalid"
	}
	log := r.log.WithValues("delivery", id)

	if req.Method != http.MethodPost {
		r.refuse(w, log, http.StatusMethodNotAllowed, resultMethodNotAllowed)
		return
	}
	if mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		r.refuse(w, log, http.StatusUnsupportedMediaType, resultUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxPayloadBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			r.refuse(w, log, http.StatusRequestEntityTooLarge, resultTooLarge)
		} else {
			r.refuse(w, log, http.StatusBadRequest, resultBadRequest)
		}
		return
	}

	secret, err := r.secret()
	if err != nil {
		// The error names the file, never its content.
		log.Error(err, "cannot read the webhook secret")
		r.results.WithLabelValues(resultError).Inc()
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !validSignature(secret, body, req.Header.Get(headerSignature)) {
		r.refuse(w, log, http.StatusUnauthorized, resultInvalidSignature)
		return
	}

	// Authenticated from here on.
	event := req.Header.Get(headerEvent)
	if !handledEvents[event] {
		log.V(1).Info("ignored a webhook", "event", sanitize(event))
		r.results.WithLabelValues(resultIgnored).Inc()
		w.WriteHeader(http.StatusOK)
		return
	}
	log.Info("received a webhook", "event", event)
	r.results.WithLabelValues(resultAccepted).Inc()
	if event == "ping" {
		w.WriteHeader(http.StatusOK)
		return
	}
	// The events that change previews are handled from #128.
	w.WriteHeader(http.StatusAccepted)
}

// refuse answers a delivery that is not authenticated, or not well formed.
// It is logged at the debug level only: anyone on the Internet can send
// them, and they must not flood the logs.
func (r *Receiver) refuse(w http.ResponseWriter, log logr.Logger, status int, result string) {
	log.V(1).Info("refused a webhook", "result", result)
	r.results.WithLabelValues(result).Inc()
	http.Error(w, http.StatusText(status), status)
}

// validSignature reports whether header is the HMAC SHA-256 of body with
// secret, as GitHub sends it: "sha256=" and 64 hexadecimal characters. The
// comparison takes the same time whatever the header, so that it tells
// nothing about the right value.
func validSignature(secret, body []byte, header string) bool {
	hexSum, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSum)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// sanitize keeps a header from GitHub short and printable before it is
// logged.
func sanitize(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}

// SecretCheck is a readiness check: a pod that cannot read its webhook
// secret would refuse every delivery, so it gets no traffic. The error says
// why, never what the secret is.
func SecretCheck(secret func() ([]byte, error)) func(*http.Request) error {
	return func(*http.Request) error {
		_, err := secret()
		return err
	}
}

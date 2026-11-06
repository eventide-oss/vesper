package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	namespacePrefix = "vesper-"
	// hashLength is the number of hex characters of the hash suffix.
	hashLength = 8
	// maxNamespaceLength is the limit of a namespace name (a DNS-1123 label).
	maxNamespaceLength = 63
	// maxLabelLength is the limit of one label of a DNS name, the part
	// between two dots.
	maxLabelLength = 63
	// maxDomainLength leaves room in a 253-character DNS name for the label
	// of a preview and its dot.
	maxDomainLength = 253 - maxLabelLength - 1
)

// previewHash returns the hash that keeps the names derived from a preview
// unique: the first characters of the SHA-256 of "<namespace>/<name>".
func previewHash(previewNamespace, name string) string {
	sum := sha256.Sum256([]byte(previewNamespace + "/" + name))
	return hex.EncodeToString(sum[:])[:hashLength]
}

// readablePart joins parts with dashes, turns dots into dashes and truncates
// the result to max characters, without a trailing dash.
func readablePart(maxLength int, parts ...string) string {
	readable := strings.ReplaceAll(strings.Join(parts, "-"), ".", "-")
	if len(readable) > maxLength {
		readable = strings.TrimRight(readable[:maxLength], "-")
	}
	return readable
}

// NamespaceName returns the name of the namespace of the preview name in
// namespace previewNamespace: "vesper-<namespace>-<name>-<hash>".
//
// A namespace is cluster-wide while previews of the same name can live in
// different namespaces, so the name includes both. The readable part is
// truncated to fit in 63 characters and dots, valid in a preview name but not
// in a namespace name, become dashes. The hash of "<namespace>/<name>" keeps
// names unique when truncation or the dash separator would make two previews
// collide, for example team-a/pr-42 and team/a-pr-42. The same preview always
// gets the same name.
func NamespaceName(previewNamespace, name string) string {
	readable := readablePart(maxNamespaceLength-len(namespacePrefix)-1-hashLength, previewNamespace, name)
	return namespacePrefix + readable + "-" + previewHash(previewNamespace, name)
}

// HostName returns the public host name of the preview name in namespace
// previewNamespace: "<name>-<namespace>-<hash>.<domain>".
//
// A wildcard DNS record or certificate, such as *.preview.example.com, covers
// a single label, so the name and the namespace share one label of at most 63
// characters, with the same hash as the namespace of the preview.
func HostName(previewNamespace, name, domain string) string {
	readable := readablePart(maxLabelLength-1-hashLength, name, previewNamespace)
	return readable + "-" + previewHash(previewNamespace, name) + "." + domain
}

// ValidatePreviewDomain checks the base domain of preview host names: a DNS
// name, lowercase, short enough to leave room for the label of a preview.
func ValidatePreviewDomain(domain string) error {
	if domain == "" {
		return errors.New("the preview domain is empty")
	}
	if errs := validation.IsDNS1123Subdomain(domain); len(errs) > 0 {
		return fmt.Errorf("preview domain %q: %s", domain, strings.Join(errs, "; "))
	}
	if len(domain) > maxDomainLength {
		return fmt.Errorf("preview domain %q: must be at most %d characters", domain, maxDomainLength)
	}
	return nil
}

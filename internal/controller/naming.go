package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	namespacePrefix = "vesper-"
	// hashLength is the number of hex characters of the hash suffix.
	hashLength = 8
	// maxNamespaceLength is the limit of a namespace name (a DNS-1123 label).
	maxNamespaceLength = 63
)

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
	sum := sha256.Sum256([]byte(previewNamespace + "/" + name))
	hash := hex.EncodeToString(sum[:])[:hashLength]

	readable := strings.ReplaceAll(previewNamespace+"-"+name, ".", "-")
	maxReadable := maxNamespaceLength - len(namespacePrefix) - 1 - hashLength
	if len(readable) > maxReadable {
		readable = strings.TrimRight(readable[:maxReadable], "-")
	}
	return namespacePrefix + readable + "-" + hash
}

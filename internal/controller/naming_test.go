package controller

import (
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestNamespaceName(t *testing.T) {
	long := strings.Repeat("a", 253) // the longest preview name
	hash := regexp.MustCompile(`-[0-9a-f]{8}$`)

	tests := []struct {
		name, namespace, preview string
		wantPrefix               string
	}{
		{"short", "team-a", "pr-42", "vesper-team-a-pr-42-"},
		{"dots become dashes", "team-a", "app.pr-42", "vesper-team-a-app-pr-42-"},
		{"long name is truncated", "team-a", long, "vesper-team-a-" + strings.Repeat("a", 40) + "-"},
		{"trailing dash after truncation is trimmed", strings.Repeat("b", 46), "pr-42", "vesper-" + strings.Repeat("b", 46) + "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NamespaceName(tt.namespace, tt.preview)
			if !strings.HasPrefix(got, tt.wantPrefix) || !hash.MatchString(got) ||
				len(got) != len(tt.wantPrefix)+hashLength {
				t.Errorf("NamespaceName(%q, %q) = %q, want %q followed by 8 hex characters",
					tt.namespace, tt.preview, got, tt.wantPrefix)
			}
			if errs := validation.IsDNS1123Label(got); len(errs) > 0 {
				t.Errorf("NamespaceName(%q, %q) = %q, not a valid namespace name: %v",
					tt.namespace, tt.preview, got, errs)
			}
			if again := NamespaceName(tt.namespace, tt.preview); again != got {
				t.Errorf("NamespaceName is not deterministic: %q then %q", got, again)
			}
		})
	}
}

func TestNamespaceNameAvoidsCollisions(t *testing.T) {
	pairs := [][2][2]string{
		// Same readable part "team-a-pr-42".
		{{"team-a", "pr-42"}, {"team", "a-pr-42"}},
		// Same readable part once dots become dashes.
		{{"team", "app.pr-42"}, {"team", "app-pr-42"}},
		// Same readable part once truncated.
		{{"team", strings.Repeat("a", 60) + "1"}, {"team", strings.Repeat("a", 60) + "2"}},
	}
	for _, p := range pairs {
		a := NamespaceName(p[0][0], p[0][1])
		b := NamespaceName(p[1][0], p[1][1])
		if a == b {
			t.Errorf("%s/%s and %s/%s both map to %q", p[0][0], p[0][1], p[1][0], p[1][1], a)
		}
	}
}

func TestHostName(t *testing.T) {
	const domain = "preview.example.com"
	long := strings.Repeat("a", 63) // the longest preview name
	tests := []struct {
		name, namespace, preview string
		wantPrefix               string
	}{
		{"short", "team-a", "pr-42", "pr-42-team-a-"},
		{"dots become dashes", "team-a", "app.pr-42", "app-pr-42-team-a-"},
		{"long name is truncated", "team-a", long, strings.Repeat("a", 54) + "-"},
		{"trailing dash after truncation is trimmed", "team-a", strings.Repeat("b", 53) + "-c", strings.Repeat("b", 53) + "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HostName(tt.namespace, tt.preview, domain)
			label, rest, _ := strings.Cut(got, ".")
			if rest != domain {
				t.Errorf("HostName = %q, want a single label in front of %s", got, domain)
			}
			wantLabel := tt.wantPrefix + previewHash(tt.namespace, tt.preview)
			if label != wantLabel {
				t.Errorf("HostName label = %q, want %q", label, wantLabel)
			}
			if errs := validation.IsDNS1123Label(label); len(errs) > 0 {
				t.Errorf("HostName label %q is not a DNS label: %v", label, errs)
			}
			if errs := validation.IsDNS1123Subdomain(got); len(errs) > 0 {
				t.Errorf("HostName = %q, not a DNS name: %v", got, errs)
			}
		})
	}
}

// TestHostNameSharesNamespaceHash checks that the host and the namespace of a
// preview carry the same hash, so that one is easy to find from the other.
func TestHostNameSharesNamespaceHash(t *testing.T) {
	host := HostName("team-a", "pr-42", "preview.example.com")
	ns := NamespaceName("team-a", "pr-42")
	hash := ns[len(ns)-hashLength:]
	if !strings.Contains(host, "-"+hash+".") {
		t.Errorf("host %q does not carry the hash %s of namespace %s", host, hash, ns)
	}
}

func TestHostNameAvoidsCollisions(t *testing.T) {
	pairs := [][2][2]string{
		{{"team-a", "pr-42"}, {"team", "pr-42-a"}}, // same readable label "pr-42-team-a"
		{{"team", "app.pr-42"}, {"team", "app-pr-42"}},
		{{"team", strings.Repeat("a", 60) + "1"}, {"team", strings.Repeat("a", 60) + "2"}},
	}
	for _, p := range pairs {
		a := HostName(p[0][0], p[0][1], "preview.example.com")
		b := HostName(p[1][0], p[1][1], "preview.example.com")
		if a == b {
			t.Errorf("%s/%s and %s/%s both map to %q", p[0][0], p[0][1], p[1][0], p[1][1], a)
		}
	}
}

func TestValidatePreviewDomain(t *testing.T) {
	tests := []struct {
		domain string
		valid  bool
	}{
		{"preview.example.com", true},
		{"localtest.me", true},
		{"", false},
		{"Preview.Example.com", false},
		{"preview_example.com", false},
		{"*.preview.example.com", false},
		{"preview.example.com.", false},
		{strings.Repeat("a.", 94) + "com", false}, // 191 characters: no room for a preview label
	}
	for _, tt := range tests {
		err := ValidatePreviewDomain(tt.domain)
		if (err == nil) != tt.valid {
			t.Errorf("ValidatePreviewDomain(%q) = %v, want valid %v", tt.domain, err, tt.valid)
		}
	}
}

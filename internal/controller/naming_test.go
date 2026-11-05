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

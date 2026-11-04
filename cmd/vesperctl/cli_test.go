package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // substring expected on stdout ("" means stdout must be empty)
		wantStderr string // substring expected on stderr ("" means stderr must be empty)
	}{
		{
			name:       "version",
			args:       []string{"version"},
			wantCode:   exitOK,
			wantStdout: "vesperctl version dev (go",
		},
		{
			name:       "no command",
			args:       nil,
			wantCode:   exitUsage,
			wantStderr: "Usage: vesperctl",
		},
		{
			name:       "unknown command",
			args:       []string{"bogus"},
			wantCode:   exitUsage,
			wantStderr: `unknown command "bogus"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			got := run(tt.args, &stdout, &stderr)

			if got != tt.wantCode {
				t.Errorf("exit code = %d, want %d", got, tt.wantCode)
			}
			checkOutput(t, "stdout", stdout.String(), tt.wantStdout)
			checkOutput(t, "stderr", stderr.String(), tt.wantStderr)
		})
	}
}

func TestRunVersionUsesBuildVersion(t *testing.T) {
	old := version
	version = "v1.2.3"
	t.Cleanup(func() { version = old })

	var stdout, stderr bytes.Buffer
	if got := run([]string{"version"}, &stdout, &stderr); got != exitOK {
		t.Fatalf("exit code = %d, want %d", got, exitOK)
	}
	if want := "vesperctl version v1.2.3 ("; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

// checkOutput fails the test if got does not contain want, or if got is not
// empty when want is empty.
func checkOutput(t *testing.T, stream, got, want string) {
	t.Helper()

	if want == "" {
		if got != "" {
			t.Errorf("%s = %q, want it empty", stream, got)
		}
		return
	}
	if !strings.Contains(got, want) {
		t.Errorf("%s = %q, want it to contain %q", stream, got, want)
	}
}

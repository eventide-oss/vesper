package main

import (
	"bytes"
	"errors"
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
			wantStderr: "vesperctl: missing command",
		},
		{
			name:       "no command prints the usage",
			args:       []string{},
			wantCode:   exitUsage,
			wantStderr: "vesperctl [command]",
		},
		{
			name:       "unknown command",
			args:       []string{"bogus"},
			wantCode:   exitUsage,
			wantStderr: `unknown command "bogus"`,
		},
		{
			name:       "unknown flag",
			args:       []string{"--bogus"},
			wantCode:   exitUsage,
			wantStderr: "unknown flag: --bogus",
		},
		{
			name:       "unknown flag on a subcommand",
			args:       []string{"version", "--bogus"},
			wantCode:   exitUsage,
			wantStderr: "unknown flag: --bogus",
		},
		{
			name:       "version rejects arguments",
			args:       []string{"version", "extra"},
			wantCode:   exitUsage,
			wantStderr: `unknown command "extra" for "vesperctl version"`,
		},
		{
			name:       "help flag",
			args:       []string{"--help"},
			wantCode:   exitOK,
			wantStdout: "Available Commands:",
		},
		{
			name:       "help command for a subcommand",
			args:       []string{"help", "version"},
			wantCode:   exitOK,
			wantStdout: "Print the version of vesperctl",
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

// failingWriter is an io.Writer whose writes always fail, like a closed pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunStdoutWriteErrorIsAFailure(t *testing.T) {
	var stderr bytes.Buffer

	got := run([]string{"version"}, failingWriter{}, &stderr)

	if got != exitFailure {
		t.Errorf("exit code = %d, want %d", got, exitFailure)
	}
	if want := "write failed"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
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

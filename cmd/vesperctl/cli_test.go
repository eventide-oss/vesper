package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
			name:       "help documents the global flags",
			args:       []string{"version", "--help"},
			wantCode:   exitOK,
			wantStdout: "Global Flags:",
		},
		{
			name:       "global flag after a subcommand",
			args:       []string{"version", "-n", "team-a"},
			wantCode:   exitOK,
			wantStdout: "vesperctl version",
		},
		{
			name:       "global flag without a value",
			args:       []string{"--namespace"},
			wantCode:   exitUsage,
			wantStderr: "flag needs an argument: --namespace",
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

			got := run(tt.args, &stdout, &stderr, emptyEnv)

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
	if got := run([]string{"version"}, &stdout, &stderr, emptyEnv); got != exitOK {
		t.Fatalf("exit code = %d, want %d", got, exitOK)
	}
	if want := "vesperctl version v1.2.3 ("; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

// emptyEnv is a getenv function for an empty environment.
func emptyEnv(string) string { return "" }

// failingWriter is an io.Writer whose writes always fail, like a closed pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunStdoutWriteErrorIsAFailure(t *testing.T) {
	var stderr bytes.Buffer

	got := run([]string{"version"}, failingWriter{}, &stderr, emptyEnv)

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

func TestExecuteTurnsPanicIntoInternalError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	opts := newGlobalOptions(emptyEnv)
	root := newRootCmd(&stdout, &stderr, opts, &fakeFactory{opts: opts, client: newFakeClient()})
	root.AddCommand(&cobra.Command{
		Use: "boom",
		RunE: func(*cobra.Command, []string) error {
			panic("unexpected nil client")
		},
	})

	code := execute(root, []string{"boom"}, &stderr)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	want := "vesperctl: internal error: unexpected nil client " +
		"(this is a bug, please report it at " + issuesURL + ")\n"
	if stderr.String() != want {
		t.Errorf("stderr =\n%q\nwant\n%q", stderr.String(), want)
	}
	if strings.Contains(stderr.String(), "goroutine ") {
		t.Errorf("stderr contains a stack trace: %q", stderr.String())
	}
}

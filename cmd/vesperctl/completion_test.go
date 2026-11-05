package main

import (
	"strings"
	"testing"
)

func TestCompletion(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // prefix expected on stdout
		wantStderr string // substring expected on stderr
	}{
		{name: "bash", args: []string{"completion", "bash"}, wantStdout: "# bash completion V2 for vesperctl"},
		{name: "zsh", args: []string{"completion", "zsh"}, wantStdout: "#compdef vesperctl\n"},
		{name: "fish", args: []string{"completion", "fish"}, wantStdout: "# fish completion for vesperctl"},
		{
			name:       "missing shell",
			args:       []string{"completion"},
			wantCode:   exitUsage,
			wantStderr: "vesperctl: accepts 1 arg(s), received 0",
		},
		{
			name:       "unsupported shell",
			args:       []string{"completion", "powershell"},
			wantCode:   exitUsage,
			wantStderr: `vesperctl: unsupported shell "powershell": valid shells are bash, zsh, fish`,
		},
		{
			// What the shell runs when the user types `vesperctl list -o <TAB>`.
			name:       "output formats",
			args:       []string{"__complete", "list", "-o", ""},
			wantStdout: "table\njson\nyaml\n:4\n",
		},
		{
			name:       "shells",
			args:       []string{"__complete", "completion", ""},
			wantStdout: "bash\nzsh\nfish\n:4\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := executeWith(t, tt.args, func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: newFakeClient()}
			})

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tt.wantCode, stderr)
			}
			if !strings.HasPrefix(stdout, tt.wantStdout) {
				t.Errorf("stdout =\n%.200s\nwant it to start with %q", stdout, tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantStderr)
			}
		})
	}
}

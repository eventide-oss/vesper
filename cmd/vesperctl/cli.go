package main

import (
	"fmt"
	"io"
	"runtime"
)

// version is set at build time with -ldflags "-X main.version=<value>".
// It stays "dev" for a plain `go build`.
var version = "dev"

// Exit codes. 2 is the Unix convention for a usage error.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `Usage: vesperctl <command>

Commands:
  version  Print the version of vesperctl
`

// run executes the command in args (the program name excluded), writes to
// stdout and stderr, and returns the process exit code. It never calls
// os.Exit, so tests can call it directly.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		// Nothing useful to do if stderr is closed, so the error is ignored.
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}

	switch args[0] {
	case "version":
		_, err := fmt.Fprintf(stdout, "vesperctl version %s (%s %s/%s)\n",
			version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return exitFailure
		}
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "vesperctl: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

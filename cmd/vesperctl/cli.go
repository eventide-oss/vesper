package main

import (
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"
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

// usageError marks an error caused by how vesperctl was invoked (unknown
// command, unknown flag, wrong number of arguments), as opposed to an error
// that happened while running a valid command. run maps it to exit code 2.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// noArgs is cobra.NoArgs, with its error marked as a usage error.
func noArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.NoArgs(cmd, args); err != nil {
		return &usageError{err}
	}
	return nil
}

// newRootCmd builds the command tree. Cobra is only used to describe commands
// and parse arguments: it prints nothing on error (SilenceErrors and
// SilenceUsage) so that run alone decides the messages and the exit code.
//
// The global flags are written into opts. Commands that talk to a cluster
// receive factory and never build a Kubernetes client themselves, so tests
// can inject a fake one.
func newRootCmd(stdout, stderr io.Writer, opts *globalOptions, factory clientFactory) *cobra.Command {
	root := &cobra.Command{
		Use:           "vesperctl",
		Short:         "Manage Vesper preview environments",
		SilenceErrors: true,
		SilenceUsage:  true,
		// An argument here is an unknown command: subcommands are matched first.
		Args: noArgs,
		RunE: func(*cobra.Command, []string) error {
			return &usageError{errors.New("missing command")}
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	// An unknown flag is a usage error. The function is inherited by subcommands.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{err}
	})
	// Shell completion is added later; do not expose Cobra's default command.
	root.CompletionOptions.DisableDefaultCmd = true
	// Persistent flags are inherited by every subcommand, as in kubectl.
	opts.addFlags(root.PersistentFlags())

	root.AddCommand(newVersionCmd(), newListCmd(factory), newGetCmd(factory))
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of vesperctl",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "vesperctl version %s (%s %s/%s)\n",
				version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}

// run executes the command in args (the program name excluded), writes to
// stdout and stderr, reads the environment through getenv, and returns the
// process exit code. It never calls os.Exit, so tests can call it directly.
func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	// Cobra reads os.Args[1:] when the arguments are nil, which would make a
	// test binary parse its own -test.* flags. An empty slice avoids that.
	if args == nil {
		args = []string{}
	}

	opts := newGlobalOptions(getenv)
	root := newRootCmd(stdout, stderr, opts, newKubeFactory(opts))
	return execute(root, args, stderr)
}

// execute runs root with args, prints the error if any, and returns the exit
// code. Tests call it with a root built around a fake clientFactory.
func execute(root *cobra.Command, args []string, stderr io.Writer) int {
	root.SetArgs(args)

	cmd, err := root.ExecuteC()
	if err == nil {
		return exitOK
	}

	// Nothing useful to do if stderr is closed, so write errors are ignored.
	var usageErr *usageError
	if errors.As(err, &usageErr) {
		_, _ = fmt.Fprintf(stderr, "vesperctl: %v\n\n%s", err, cmd.UsageString())
		return exitUsage
	}
	_, _ = fmt.Fprintf(stderr, "vesperctl: %v\n", err)
	return exitFailure
}

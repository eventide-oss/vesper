package main

import (
	"fmt"
	"slices"

	"github.com/spf13/cobra"
)

// shells lists the shells `completion` supports.
var shells = []string{"bash", "zsh", "fish"}

// newCompletionCmd replaces Cobra's default completion command, so that the
// supported shells, the help and the exit code of a usage error are ours.
//
// The scripts are thin: on each TAB the shell runs the hidden
// `vesperctl __complete ...` command, which Cobra adds, and shows what it
// prints. The completion logic stays in Go.
func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion bash|zsh|fish",
		Short: "Print a shell completion script",
		Long: `Print a script that makes your shell complete vesperctl commands, flags
and values. Load it in your shell configuration:

  bash  (needs the bash-completion package)
        echo 'source <(vesperctl completion bash)' >> ~/.bashrc
  zsh   echo 'source <(vesperctl completion zsh)' >> ~/.zshrc
  fish  vesperctl completion fish > ~/.config/fish/completions/vesperctl.fish

Then start a new shell.`,
		ValidArgs:             shells,
		Args:                  shellArg,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, out := cmd.Root(), cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			default: // fish: shellArg accepts nothing else.
				return root.GenFishCompletion(out, true)
			}
		},
	}
}

// shellArg requires exactly one supported shell, and marks any violation as
// a usage error.
func shellArg(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return &usageError{err}
	}
	if !slices.Contains(shells, args[0]) {
		return &usageError{fmt.Errorf("unsupported shell %q: valid shells are bash, zsh, fish", args[0])}
	}
	return nil
}

// Package cli implements the strux command line. A command computes a result and never writes
// output itself; Run writes every result the same way, as YAML or, with --json, as JSON.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/altakleos/strux/internal/version"
)

// Exit codes.
const (
	exitOK    = 0 // the command succeeded
	exitError = 1 // the command failed
	exitUsage = 2 // the command line is invalid
)

// Run runs strux with args, which exclude the program name. It writes the result to stdout and
// diagnostics to stderr, and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	out := &output{w: stdout}
	root := newRoot(out)
	if args == nil {
		args = []string{} // cobra reads os.Args when given nil
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if cmd != nil && cmd.Name() == cobra.ShellCompRequestCmd {
		// strux has no shell completion. root's pre-run hook stops the hidden command cobra adds
		// to answer it, but cobra rejects that command's arguments before the hook runs.
		err = unknownCommand(root, cmd.CalledAs())
	}
	if err == nil {
		err = out.err
	}
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(stderr, "strux: %v\n", err)
	if errors.As(err, new(usageError)) {
		return exitUsage
	}
	return exitError
}

func newRoot(out *output) *cobra.Command {
	var showVersion bool
	root := &cobra.Command{
		Use:   "strux [command]",
		Short: "Strux keeps agent work accountable.",
		Args:  noArgs,
		// cobra adds a hidden command to answer shell completion when the command line names it,
		// and strux has no shell completion.
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			if c.Name() == cobra.ShellCompRequestCmd {
				return unknownCommand(c.Root(), c.CalledAs())
			}
			return nil
		},
		RunE: out.runs(func(c *cobra.Command, _ []string) (any, error) {
			if showVersion {
				return version.Read(), nil
			}
			return describe(c), nil
		}),
		CompletionOptions:          cobra.CompletionOptions{DisableDefaultCmd: true},
		SilenceErrors:              true,
		SilenceUsage:               true,
		SuggestionsMinimumDistance: 2,
	}
	root.PersistentFlags().BoolVar(&out.json, "json", false, "Write the result as JSON.")
	root.PersistentFlags().BoolP("help", "h", false, "Show help.")
	root.Flags().BoolVar(&showVersion, "version", false, "Show this build's version.")
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error { return usageError{c, err} })
	// cobra answers --help itself, before it checks the command's arguments, and has no way to
	// return an error from it; Run reports what goes wrong.
	root.SetHelpFunc(func(c *cobra.Command, _ []string) {
		if out.err != nil {
			return
		}
		if out.err = c.ValidateArgs(c.Flags().Args()); out.err == nil {
			out.err = out.write(describe(c))
		}
	})
	root.SetHelpCommand(newHelp(out))
	root.AddCommand(newVersion(out))
	return root
}

func newHelp(out *output) *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Show help for strux or one of its commands.",
		Args:  cobra.ArbitraryArgs,
		RunE: out.runs(func(c *cobra.Command, args []string) (any, error) {
			target, rest, err := c.Root().Find(args)
			if err != nil {
				return nil, usageError{c, err}
			}
			if len(rest) > 0 {
				return nil, unknownCommand(target, rest[0])
			}
			return describe(target), nil
		}),
	}
}

func newVersion(out *output) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show this build's version.",
		Args:  noArgs,
		RunE: out.runs(func(*cobra.Command, []string) (any, error) {
			return version.Read(), nil
		}),
	}
}

// help describes a command: what it does, how to call it, and its commands and flags. It is
// generated from the command tree, so it cannot drift from what the command accepts.
type help struct {
	Command  string        `json:"command"`
	Summary  string        `json:"summary"`
	Usage    string        `json:"usage"`
	Commands []commandHelp `json:"commands,omitempty"`
	Flags    []flagHelp    `json:"flags,omitempty"`
}

type commandHelp struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type flagHelp struct {
	Name  string `json:"name"`
	Short string `json:"short,omitempty"`
	Usage string `json:"usage"`
}

func describe(c *cobra.Command) help {
	h := help{Command: c.CommandPath(), Summary: c.Short}
	for _, sub := range c.Commands() {
		if !sub.Hidden && (sub.Runnable() || sub.HasAvailableSubCommands()) {
			h.Commands = append(h.Commands, commandHelp{sub.Name(), sub.Short})
		}
	}
	add := func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		fh := flagHelp{Name: "--" + f.Name, Usage: f.Usage}
		if f.Shorthand != "" {
			fh.Short = "-" + f.Shorthand
		}
		h.Flags = append(h.Flags, fh)
	}
	// Listing the flags merges the inherited ones into the command's, which the usage line
	// counts; cobra merges them only when it parses a command's own command line.
	c.LocalFlags().VisitAll(add)
	c.InheritedFlags().VisitAll(add)
	sort.Slice(h.Flags, func(i, j int) bool { return h.Flags[i].Name < h.Flags[j].Name })
	h.Usage = c.UseLine()
	return h
}

// usageError is an invalid command line, such as an unknown command or flag. It names the help
// for the command whose valid forms it breaks.
type usageError struct {
	cmd *cobra.Command
	err error
}

func (e usageError) Error() string {
	see := e.cmd.Root().Name() + " help"
	if e.cmd.HasParent() {
		see += strings.TrimPrefix(e.cmd.CommandPath(), e.cmd.Root().Name())
	}
	return fmt.Sprintf("%v (see %q)", e.err, see)
}

func (e usageError) Unwrap() error { return e.err }

// noArgs accepts no arguments. On a command with commands of its own, an argument is an unknown
// command, or a command that follows "--", which ends the commands and flags.
func noArgs(c *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return nil
	case c.HasAvailableSubCommands():
		if sub, _, err := c.Find(args[:1]); err == nil && sub != c {
			return usageError{c, fmt.Errorf("command %q must come before \"--\"", args[0])}
		}
		return unknownCommand(c, args[0])
	default:
		return usageError{c, fmt.Errorf("%s takes no arguments", c.CommandPath())}
	}
}

func unknownCommand(c *cobra.Command, name string) error {
	msg := fmt.Sprintf("unknown command %q", name)
	if c.HasParent() {
		msg += fmt.Sprintf(" for %q", c.CommandPath())
	}
	if s := c.SuggestionsFor(name); name != "" && len(s) > 0 {
		msg += fmt.Sprintf("; did you mean %q?", s[0])
	}
	return usageError{c, errors.New(msg)}
}

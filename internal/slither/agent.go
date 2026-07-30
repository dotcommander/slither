package slither

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const agentSchema = "slither.agent/v1"

type agentOptions struct {
	repo     string
	outcomes string
}

func runAgent(ctx context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		printCommandHelp(stdout, "agent")
		return nil
	}
	opts, err := resolveAgentOptions(args)
	if err != nil {
		return err
	}
	info, err := os.Stat(opts.repo)
	if err != nil {
		return fmt.Errorf("stat agent repository: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agent repository is not a directory: %s", opts.repo)
	}
	var writer outcomeWriter
	if opts.outcomes != "" {
		writer, err = outcomeWriterForPath(opts.outcomes)
		if err != nil {
			return fmt.Errorf("initialize outcome writer: %w", err)
		}
	}
	return runAgentProtocol(ctx, opts.repo, writer, bufio.NewReader(stdin), stdout)
}

func resolveAgentOptions(args []string) (agentOptions, error) {
	for index, arg := range args {
		if arg == "--outcomes" && index+1 == len(args) {
			return agentOptions{}, usageError("agent", errors.New("--outcomes requires a path"))
		}
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var outcomes string
	fs.StringVar(&outcomes, "outcomes", "", "private outcome ledger path")
	if err := fs.Parse(normalizeAgentArgs(args)); err != nil {
		return agentOptions{}, usageError("agent", err)
	}
	if fs.NArg() > 1 {
		return agentOptions{}, usageError("agent", errors.New("agent accepts at most one repository path"))
	}
	repo := "."
	if fs.NArg() == 1 {
		repo = fs.Arg(0)
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return agentOptions{}, fmt.Errorf("resolve agent repository: %w", err)
	}
	return agentOptions{repo: filepath.Clean(absRepo), outcomes: outcomes}, nil
}

func normalizeAgentArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			if arg == "--outcomes" && index+1 < len(args) {
				index++
				flags = append(flags, args[index])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...)
}

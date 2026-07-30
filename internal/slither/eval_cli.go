package slither

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type evalOptions struct {
	outcomes string
	reports  []string
	out      string
	markdown bool
}

type repeatedPathFlag []string

func (f *repeatedPathFlag) String() string { return "" }
func (f *repeatedPathFlag) Set(value string) error {
	if value == "" {
		return errors.New("path is empty")
	}
	*f = append(*f, value)
	return nil
}

type requiredPathFlag struct {
	value string
	set   bool
}

func (f *requiredPathFlag) String() string { return f.value }
func (f *requiredPathFlag) Set(value string) error {
	if f.set || value == "" {
		return errors.New("path must be supplied exactly once")
	}
	f.value, f.set = value, true
	return nil
}

func runEval(ctx context.Context, args []string, stdout io.Writer) error {
	opts, err := resolveEvalOptions(args)
	if err != nil {
		return err
	}
	if err := rejectEvalOutputAlias(opts.out, append([]string{opts.outcomes}, opts.reports...)); err != nil {
		return err
	}
	result, err := evaluateOutcomeFiles(ctx, opts.outcomes, opts.reports)
	if err != nil {
		return err
	}
	var output []byte
	if opts.markdown {
		output = []byte(renderEvalMarkdown(result))
	} else {
		output, err = json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode evaluation: %w", err)
		}
		output = append(output, '\n')
	}
	if opts.out == "-" {
		n, err := stdout.Write(output)
		if err != nil {
			return err
		}
		if n != len(output) {
			return io.ErrShortWrite
		}
		return nil
	}
	if err := atomicWriteFile(opts.out, output, 0o600); err != nil {
		return fmt.Errorf("write evaluation: %w", err)
	}
	return nil
}

func rejectEvalOutputAlias(out string, inputs []string) error {
	if out == "-" {
		return nil
	}
	outputPath, err := filepath.Abs(filepath.Clean(out))
	if err != nil {
		return fmt.Errorf("resolve evaluation output: %w", err)
	}
	outputInfo, outputErr := os.Stat(outputPath)
	if outputErr != nil && !errors.Is(outputErr, os.ErrNotExist) {
		return fmt.Errorf("stat evaluation output: %w", outputErr)
	}
	for _, input := range inputs {
		inputPath, err := filepath.Abs(filepath.Clean(input))
		if err != nil {
			return fmt.Errorf("resolve evaluation input: %w", err)
		}
		if inputPath == outputPath {
			return errors.New("evaluation output aliases an input")
		}
		if outputErr != nil {
			continue
		}
		inputInfo, err := os.Stat(inputPath)
		if err == nil && os.SameFile(outputInfo, inputInfo) {
			return errors.New("evaluation output aliases an input")
		}
	}
	return nil
}

func resolveEvalOptions(args []string) (evalOptions, error) {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var outcomes requiredPathFlag
	var reports repeatedPathFlag
	var out string
	var jsonOutput bool
	var markdownOutput bool
	fs.Var(&outcomes, "outcomes", "outcome JSONL ledger")
	fs.Var(&reports, "report", "report JSON input; repeat")
	fs.BoolVar(&jsonOutput, "json", false, "emit JSON")
	fs.BoolVar(&markdownOutput, "markdown", false, "emit privacy-minimal Markdown")
	fs.StringVar(&out, "out", "-", "output path, or - for stdout")
	if err := fs.Parse(args); err != nil {
		return evalOptions{}, usageError("eval", err)
	}
	if fs.NArg() != 0 || !outcomes.set || len(reports) == 0 || jsonOutput == markdownOutput {
		return evalOptions{}, usageError("eval", errors.New("eval requires --outcomes, one or more --report, and exactly one of --json or --markdown with no positional arguments"))
	}
	return evalOptions{outcomes: outcomes.value, reports: append([]string(nil), reports...), out: out, markdown: markdownOutput}, nil
}

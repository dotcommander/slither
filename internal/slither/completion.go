package slither

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

func runCompletion(args []string, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "bash" && args[0] != "zsh") {
		return usageError("completion", errors.New("completion requires exactly one shell: bash or zsh"))
	}
	if args[0] == "bash" {
		_, err := io.WriteString(stdout, bashCompletion())
		return err
	}
	_, err := io.WriteString(stdout, zshCompletion())
	return err
}

func completionCommands() string {
	parts := make([]string, 0, len(commandSpecs()))
	for _, spec := range commandSpecs() {
		parts = append(parts, spec.Name)
	}
	return strings.Join(parts, " ")
}

func completionFlags(command string) string {
	spec, ok := findCommandSpec(command)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(spec.Flags)+2)
	for _, flag := range spec.Flags {
		parts = append(parts, flag.Name)
	}
	return strings.Join(append(parts, "--help", "-h"), " ")
}

func bashCompletion() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# bash completion for slither\n_slither() {\n  local cur prev cmd\n  COMPREPLY=()\n  cur=${COMP_WORDS[COMP_CWORD]}\n  cmd=${COMP_WORDS[1]}\n  if [[ $COMP_CWORD -eq 1 ]]; then\n    COMPREPLY=( $(compgen -W '%s help --help -h' -- \"$cur\") )\n    return 0\n  fi\n", completionCommands())
	fmt.Fprint(&b, "  case \"$cmd\" in\n")
	for _, spec := range commandSpecs() {
		fmt.Fprintf(&b, "    %s) COMPREPLY=( $(compgen -W '%s' -- \"$cur\") ) ;;\n", spec.Name, completionFlags(spec.Name))
	}
	fmt.Fprintf(&b, "    help) COMPREPLY=( $(compgen -W '%s' -- \"$cur\") ) ;;\n", completionCommands())
	fmt.Fprint(&b, "  esac\n}\ncomplete -F _slither slither\n")
	return b.String()
}

func zshCompletion() string {
	var b strings.Builder
	fmt.Fprint(&b, "#compdef slither\n_slither() {\n  local -a commands help_commands\n  commands=(\n")
	for _, spec := range commandSpecs() {
		fmt.Fprintf(&b, "    '%s:%s'\n", spec.Name, spec.Summary)
	}
	fmt.Fprint(&b, "    'help:show command help'\n    '--help:show root help'\n    '-h:show root help'\n")
	fmt.Fprint(&b, "  )\n  help_commands=(\n")
	for _, spec := range commandSpecs() {
		fmt.Fprintf(&b, "    '%s:%s'\n", spec.Name, spec.Summary)
	}
	fmt.Fprint(&b, "  )\n  if (( CURRENT == 2 )); then\n    _describe 'command' commands\n    return\n  fi\n  case $words[2] in\n")
	for _, spec := range commandSpecs() {
		fmt.Fprintf(&b, "    %s) _arguments '*: :(%s)' ;;\n", spec.Name, completionFlags(spec.Name))
	}
	fmt.Fprint(&b, "    help) _describe 'command' help_commands ;;\n")
	fmt.Fprint(&b, "  esac\n}\ncompdef _slither slither\n")
	return b.String()
}

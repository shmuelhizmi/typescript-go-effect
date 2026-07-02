package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/cmds"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// Shell completion is dynamic: `tsagent completion <shell>` prints a small
// static script that forwards the current command line to the hidden
// `tsagent __complete -- <words…>` endpoint, which derives candidates from the
// live command registry and flag definitions. Nothing about the completion is
// hard-coded per command, so it never drifts from --help.

// Completion directives, matching cobra's bitmask so the shell glue is familiar.
const (
	dirDefault = 0 // shell may add file completion
	dirNoFiles = 4 // shell must not add file completion
)

// candidate is one completion suggestion. desc is shown inline by fish and zsh.
type candidate struct {
	value string
	desc  string
}

// runComplete is the hidden `__complete` endpoint. args is the raw word list
// (after a leading "--"), whose final element is the word being completed.
func runComplete(args []string, w io.Writer) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	cur := ""
	prior := args
	if len(args) > 0 {
		cur = args[len(args)-1]
		prior = args[:len(args)-1]
	}
	cands, directive := completeFor(prior, cur)
	for _, c := range cands {
		if c.desc != "" {
			fmt.Fprintf(w, "%s\t%s\n", c.value, c.desc)
		} else {
			fmt.Fprintln(w, c.value)
		}
	}
	fmt.Fprintf(w, ":%d\n", directive)
	return cli.ExitOK
}

// completeFor resolves the candidates for a partially-typed command line.
// prior is the fully-typed words; cur is the word under completion.
func completeFor(prior []string, cur string) ([]candidate, int) {
	// 1) Inline `--flag=value`.
	if strings.HasPrefix(cur, "--") && strings.Contains(cur, "=") {
		eq := strings.IndexByte(cur, '=')
		name := strings.TrimLeft(cur[:eq], "-")
		if vals, ok := flagValueCandidates(name, prior); ok {
			return prefixedValues(cur[:eq+1], cur[eq+1:], vals), dirNoFiles
		}
		return nil, dirDefault
	}

	// 2) Value for a preceding non-bool flag (`--flag <value>`).
	if len(prior) > 0 {
		last := prior[len(prior)-1]
		if strings.HasPrefix(last, "-") && !strings.Contains(last, "=") {
			name := strings.TrimLeft(last, "-")
			if isValueFlag(name, prior) {
				vals, ok := flagValueCandidates(name, prior)
				if !ok {
					// A value flag we have no candidates for (paths, numbers): let
					// the shell complete files.
					return nil, dirDefault
				}
				// Comma-list values (e.g. `--include a,b`): complete the tail.
				if i := strings.LastIndexByte(cur, ','); i >= 0 {
					return prefixedValues(cur[:i+1], cur[i+1:], vals), dirNoFiles
				}
				return filterCands(vals, cur), dirNoFiles
			}
		}
	}

	// 3) Flag names.
	if strings.HasPrefix(cur, "-") {
		cmd, _ := resolveCmd(prior)
		return filterCands(flagCandidates(cmd), cur), dirNoFiles
	}

	// 4) Command names.
	switch {
	case len(prior) == 0:
		return filterCands(familyCandidates(), cur), dirNoFiles
	case len(prior) == 1 && prior[0] == "completion":
		return filterCands(shellCandidates(), cur), dirNoFiles
	case len(prior) == 1 && hasSubcommands(prior[0]):
		// A family that also has a direct (empty-name) command — e.g. `check`,
		// `edit` — accepts positional path args here, so still allow files.
		directive := dirNoFiles
		if _, ok := cli.Lookup(prior[0], ""); ok {
			directive = dirDefault
		}
		return filterCands(subcommandCandidates(prior[0]), cur), directive
	}

	// 5) Positional argument: let the shell complete files.
	return nil, dirDefault
}

// resolveCmd resolves the command addressed by the typed words, mirroring the
// dispatcher's lookup (family + optional subcommand, falling back to a
// single-command family).
func resolveCmd(prior []string) (cli.Command, bool) {
	if len(prior) == 0 {
		return cli.Command{}, false
	}
	cmd, _, ok := lookupCommand(prior[0], prior[1:])
	return cmd, ok
}

func buildFlagSet(cmd cli.Command) *flag.FlagSet {
	fs := flag.NewFlagSet("complete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerGlobalFlags(fs, cmd.Family != "serve")
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}
	return fs
}

// flagCandidates lists the global + command-specific flags as `--name`, using
// each flag's usage first line as the description.
func flagCandidates(cmd cli.Command) []candidate {
	var out []candidate
	buildFlagSet(cmd).VisitAll(func(f *flag.Flag) {
		out = append(out, candidate{value: "--" + f.Name, desc: firstLine(f.Usage)})
	})
	return out
}

// isValueFlag reports whether the named flag of the resolved command takes a
// value (i.e. is not a boolean flag).
func isValueFlag(name string, prior []string) bool {
	cmd, _ := resolveCmd(prior)
	f := buildFlagSet(cmd).Lookup(name)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}

// flagValueCandidates returns the known value set for an enum-like flag.
func flagValueCandidates(name string, prior []string) ([]candidate, bool) {
	switch name {
	case "format":
		return plain("json", "text", "ndjson"), true
	case "connect":
		return plain("never", "auto", "require"), true
	case "include":
		if cmd, ok := resolveCmd(prior); ok && cmd.Family == "report" {
			return plain(cmds.ReportProviderKeys()...), true
		}
	}
	return nil, false
}

func familyCandidates() []candidate {
	seen := map[string]bool{}
	var out []candidate
	for _, c := range cli.Commands() {
		if seen[c.Family] {
			continue
		}
		seen[c.Family] = true
		out = append(out, candidate{value: c.Family, desc: familyDesc(c.Family)})
	}
	return out
}

// familyDesc describes a family by its single default command's summary, when
// it has one (e.g. `check`, `edit`, `completion`).
func familyDesc(family string) string {
	if cmd, ok := cli.Lookup(family, ""); ok {
		return firstLine(cmd.Summary)
	}
	return ""
}

func subcommandCandidates(family string) []candidate {
	var out []candidate
	for _, c := range cli.Commands() {
		if c.Family == family && c.Name != "" {
			out = append(out, candidate{value: c.Name, desc: firstLine(c.Summary)})
		}
	}
	return out
}

func hasSubcommands(family string) bool {
	for _, c := range cli.Commands() {
		if c.Family == family && c.Name != "" {
			return true
		}
	}
	return false
}

func shellCandidates() []candidate {
	return []candidate{
		{value: "bash", desc: "print the bash completion script"},
		{value: "zsh", desc: "print the zsh completion script"},
		{value: "fish", desc: "print the fish completion script"},
	}
}

func plain(values ...string) []candidate {
	out := make([]candidate, len(values))
	for i, v := range values {
		out[i] = candidate{value: v}
	}
	return out
}

func filterCands(vals []candidate, cur string) []candidate {
	if cur == "" {
		return vals
	}
	var out []candidate
	for _, v := range vals {
		if strings.HasPrefix(v.value, cur) {
			out = append(out, v)
		}
	}
	return out
}

func prefixedValues(prefix, partial string, vals []candidate) []candidate {
	var out []candidate
	for _, v := range vals {
		if strings.HasPrefix(v.value, partial) {
			out = append(out, candidate{value: prefix + v.value, desc: v.desc})
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// ---- the `completion` command ----

func init() {
	cli.Register(cli.Command{
		Family:       "completion",
		Summary:      "Print a shell completion script (bash, zsh, or fish)",
		NeedsProgram: false,
		Run: func(_ context.Context, _ *core.Workspace, _ any, args []string) (any, error) {
			if len(args) != 1 {
				return nil, cli.UsageErrorf("usage: tsagent completion <bash|zsh|fish>")
			}
			script, ok := completionScripts[args[0]]
			if !ok {
				return nil, cli.UsageErrorf("unknown shell %q (want bash, zsh, or fish)", args[0])
			}
			return rawText(script), nil
		},
	})
}

// rawText is a command result printed verbatim (the completion script), with no
// JSON envelope in text mode.
type rawText string

var _ cli.Texter = rawText("")

func (r rawText) WriteText(w io.Writer) error {
	_, err := io.WriteString(w, string(r))
	return err
}

var completionScripts = map[string]string{
	"bash": bashCompletion,
	"zsh":  zshCompletion,
	"fish": fishCompletion,
}

const bashCompletion = `# bash completion for tsagent
# install: tsagent completion bash > /etc/bash_completion.d/tsagent
#      or: tsagent completion bash > ~/.tsagent-completion.bash && echo 'source ~/.tsagent-completion.bash' >> ~/.bashrc
_tsagent() {
    local cur directive=0 line
    local -a args
    cur="${COMP_WORDS[COMP_CWORD]}"
    args=("${COMP_WORDS[@]:1:COMP_CWORD}")
    COMPREPLY=()
    while IFS= read -r line; do
        [[ -z $line ]] && continue
        if [[ $line == :* ]]; then directive="${line#:}"; continue; fi
        COMPREPLY+=("${line%%$'\t'*}")
    done < <(tsagent __complete -- "${args[@]}" 2>/dev/null)
    if (( (directive & 4) == 0 )); then
        while IFS= read -r line; do COMPREPLY+=("$line"); done < <(compgen -f -- "$cur")
    fi
}
complete -F _tsagent tsagent
`

const zshCompletion = `#compdef tsagent
# zsh completion for tsagent
# install: tsagent completion zsh > "${fpath[1]}/_tsagent"  (then restart zsh)
_tsagent() {
    local line val desc directive=0
    local -a comps
    while IFS= read -r line; do
        [[ -z $line ]] && continue
        if [[ $line == :* ]]; then directive="${line#:}"; continue; fi
        val="${line%%$'\t'*}"
        if [[ $line == *$'\t'* ]]; then desc="${line#*$'\t'}"; else desc="$val"; fi
        comps+=("${val}:${desc}")
    done < <(tsagent __complete -- "${words[@]:1}" 2>/dev/null)
    if (( ${#comps} )); then
        _describe -t tsagent 'tsagent' comps
    fi
    if (( (directive & 4) == 0 )); then
        _files
    fi
}
compdef _tsagent tsagent
`

const fishCompletion = `# fish completion for tsagent
# install: tsagent completion fish > ~/.config/fish/completions/tsagent.fish
function __tsagent_complete
    set -l tokens (commandline -opc)
    set -e tokens[1]
    set -l cur (commandline -ct)
    for line in (tsagent __complete -- $tokens $cur 2>/dev/null)
        string match -q -- ':*' $line; and continue
        printf '%s\n' $line
    end
end
complete -c tsagent -a '(__tsagent_complete)'
`

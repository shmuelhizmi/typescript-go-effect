package cli

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// Command describes one `tsagent <family> <name>` command. Handlers are
// transport-agnostic pure functions so the same registry can later be
// dispatched over JSON-RPC by the session daemon.
type Command struct {
	Family  string
	Name    string
	Summary string
	// Flags registers command-specific flags on fs and returns the flags
	// struct that will be passed back to Run. May be nil.
	Flags func(fs *flag.FlagSet) any
	// Run executes the command. ws is nil when NeedsProgram is false.
	Run func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error)
	// NeedsProgram is false for pure commands (e.g. daemon admin).
	NeedsProgram bool
}

var (
	registryMu sync.Mutex
	registry   = map[string]Command{}
)

func commandKey(family string, name string) string {
	return family + " " + name
}

// Register adds a command to the global registry. It is intended to be called
// from init() functions in the cmds package and panics on duplicates.
func Register(c Command) {
	registryMu.Lock()
	defer registryMu.Unlock()
	key := commandKey(c.Family, c.Name)
	if _, ok := registry[key]; ok {
		panic(fmt.Sprintf("tsagent: duplicate command %q", key))
	}
	registry[key] = c
}

// Lookup finds a registered command by family and name.
func Lookup(family string, name string) (Command, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	c, ok := registry[commandKey(family, name)]
	return c, ok
}

// Commands returns all registered commands sorted by family then name.
func Commands() []Command {
	registryMu.Lock()
	defer registryMu.Unlock()
	all := make([]Command, 0, len(registry))
	for _, c := range registry {
		all = append(all, c)
	}
	slices.SortFunc(all, func(a, b Command) int {
		if c := strings.Compare(a.Family, b.Family); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return all
}

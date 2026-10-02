package agentrunner

import (
	"fmt"
	"sort"
	"strings"
)

// Options is the operator-declared part of an adapter's configuration.
type Options struct {
	RepoRoot  string
	Command   string
	ExtraArgs []string
	Env       map[string]string
}

var registry = map[string]func(Options) Adapter{}

// Register adds an adapter factory. It panics on a duplicate name or a nil
// factory: both are programming errors visible at init.
func Register(name string, f func(Options) Adapter) {
	if f == nil {
		panic("agentrunner: nil factory for adapter " + name)
	}
	if _, dup := registry[name]; dup {
		panic("agentrunner: adapter " + name + " registered twice")
	}
	registry[name] = f
}

// New builds the named adapter.
func New(name string, o Options) (Adapter, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown backend adapter %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	return f(o), nil
}

// Names returns the registered adapter names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

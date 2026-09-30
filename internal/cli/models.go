package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/models"
)

func loadBackend(configPath, backendConfigPath string) (agentrunner.BackendConfig, error) {
	path := backendConfigPath
	if path == "" {
		path = configPath
	}
	if path == "" {
		return agentrunner.BackendConfig{}, errors.New("--config or --backend-config is required")
	}
	return agentrunner.LoadBackendConfig(path)
}

// ModelsList prints a backend's tiers, models and per-stage default models.
func ModelsList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to a shem.yaml")
	backendConfigPath := fs.String("backend-config", "", "path to a backend config document")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	b, err := loadBackend(*configPath, *backendConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	selection := "no"
	if b.SupportsSelection {
		selection = "yes"
	}
	fmt.Fprintf(stdout, "%s  (selection: %s)\n", b.Adapter, selection)
	fmt.Fprintf(stdout, "tiers: %s\n", joinTiers(b.Tiers))

	w := tabwriter.NewWriter(stdout, 2, 8, 2, ' ', 0)
	for _, m := range b.Models {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", m.ID, m.Tier, m.Label)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	resolved, _ := models.Resolve(b.Catalog, nil)
	fmt.Fprintln(stdout, "stage defaults:")
	w = tabwriter.NewWriter(stdout, 2, 8, 2, ' ', 0)
	for _, st := range models.Stages {
		id := resolved[st]
		if id == "" {
			id = "(vendor default)"
		}
		fmt.Fprintf(w, "  %s\t%s\n", st, id)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func joinTiers(tiers []models.Tier) string {
	out := ""
	for i, t := range tiers {
		if i > 0 {
			out += ", "
		}
		out += string(t)
	}
	return out
}

// ModelsProbe checks each catalog model against the vendor CLI and
// prints the verified backend block.
func ModelsProbe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to a shem.yaml")
	backendConfigPath := fs.String("backend-config", "", "path to a backend config document")
	timeout := fs.Duration("timeout", 60*time.Second, "per-model timeout")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	b, err := loadBackend(*configPath, *backendConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runner, err := b.NewAdapter("")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	dir, err := os.MkdirTemp("", "golem-probe-")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// The agent account runs the session and has to enter this directory.
	if err := os.Chmod(dir, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if acct := agentenv.User(); acct != nil {
		if err := os.Chown(dir, int(acct.UID), int(acct.GID)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	verified := b
	verified.Models = nil
	for _, m := range b.Models {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		err := runner.RunPhase(ctx, dir, "reply with the single word ok", io.Discard, m.ID)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "  %s: unavailable (%v)\n", m.ID, err)
			continue
		}
		verified.Models = append(verified.Models, m)
	}
	// A block with no models would be pasted in and silently disable selection.
	if len(verified.Models) == 0 {
		fmt.Fprintf(stderr, "no model in the %s catalog answered\n", b.Adapter)
		return 1
	}
	// Printed, never written: shem.yaml is the operator's file.
	data, err := verified.MarshalDocument()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprint(stdout, string(data))
	return 0
}

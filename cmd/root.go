package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-helm/internal/cubclient"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Version returns the plugin version (set via ldflags at build time).
func Version() string { return version }

// cub is the ConfigHub API client, initialized by the helm command's
// PersistentPreRunE for every subcommand except template (which is offline).
var cub *cubclient.Client

// Shared flags across the write subcommands.
var (
	quiet bool
	wait  bool
)

// NewRootCmd builds the helm command tree. The plugin contributes the single
// top-level command "helm"; cub invokes this binary with the subcommand as the
// first argument (e.g. "cub helm install ..." runs "<binary> install ...").
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "helm",
		Short: "Install Helm charts as ConfigHub components",
		Long: `Install Helm charts as ConfigHub components.

A chart is rendered entirely client-side and its output becomes units in the
component's base variant space (<component>-base). The chart reference, values,
and options are recorded as a HelmSource unit in the component's helm source
space (<component>-helm), which is the source of truth for upgrades.

Rendering never contacts a cluster: hooks are dropped unless --include-hooks is
set, the lookup template function returns nothing, and capabilities are Helm's
defaults. Charts that depend on those are out of scope; charts that render
cleanly client-side work fully.

Deployments are created from the base with 'cub variant create' and updated
with 'cub variant promote'; helm commands never touch them.`,
		Version:      version,
		SilenceUsage: true,
	}
	root.AddCommand(newInstallCmd(), newUpgradeCmd(), newTemplateCmd(), newVersionCmd())
	return root
}

func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// ensureClient initializes the shared ConfigHub client. It is used as the
// PersistentPreRunE for subcommands that talk to the server.
func ensureClient(cmd *cobra.Command, _ []string) error {
	c, err := cubclient.New(context.Background())
	if err != nil {
		return err
	}
	cub = c
	return nil
}

// tprint writes an informational line to stdout, normalizing trailing newlines.
func tprint(format string, args ...any) {
	format = strings.Trim(format, "\n") + "\n"
	fmt.Printf(format, args...)
}

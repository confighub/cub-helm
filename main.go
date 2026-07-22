package main

import (
	"fmt"
	"os"

	"github.com/confighub/sdk/core/plugin"

	"github.com/confighub/cub-helm/cmd"
)

func main() {
	// When cub installs or upgrades this plugin it invokes the binary as a
	// hook; HandleHook writes cub-plugin.yaml into CUB_PLUGIN_DIR and we exit
	// without running the normal command tree.
	manifest := plugin.Manifest{
		Name:    "helm",
		Version: cmd.Version(),
		Commands: []plugin.Command{{
			Name:    "helm",
			Summary: "Install Helm charts as ConfigHub components",
		}},
	}
	if handled, err := plugin.HandleHook(manifest); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cmd.Execute()
}

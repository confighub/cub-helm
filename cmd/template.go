package cmd

import (
	"github.com/spf13/cobra"
)

func newTemplateCmd() *cobra.Command {
	var args installArgs

	cmd := &cobra.Command{
		Use:   "template <release-name> <chart-ref>",
		Short: "Preview the units installing a Helm chart would create",
		Long: `Render a Helm chart client-side and show what 'cub helm install' would do
with it: the units the upload would create in the component's base space, and
those it would update or empty when the release is already installed. Nothing is
written. It takes the same flags as install, and is the same as
'cub helm install --dry-run'.

The server decides the units, so this needs a server connection. To see the
rendered YAML itself, use 'helm template'.

Examples:
` + "```" + `
  # Preview installing a chart as component "cubbychat"
  cub helm template cubbychat oci://ghcr.io/confighub/charts/cubbychat
` + "```" + `
`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       ensureClient,
		RunE: func(cmd *cobra.Command, positional []string) error {
			return runInstall(&args, positional[0], positional[1], true)
		},
	}

	addInstallFlags(cmd, &args)
	return cmd
}

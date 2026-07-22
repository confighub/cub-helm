package cmd

import (
	"encoding/base64"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/confighub/sdk/bridge-impl/helmutils"
)

func newUpgradeCmd() *cobra.Command {
	var args struct {
		component       string
		namespace       string
		createNamespace bool
		valuesFiles     []string
		set             []string
		version         string
		repo            string
		includeHooks    bool
		skipCRDs        bool
	}

	cmd := &cobra.Command{
		Use:   "upgrade <release-name>",
		Short: "Upgrade a Helm release's base units from its HelmSource",
		Long: `Upgrade a Helm release installed with 'cub helm install'.

The release's HelmSource unit is the source of truth: flags patch it first
(--version replaces the chart version constraint; -f/--set replace the stored
values), then the chart is re-rendered from the HelmSource alone and the base
space's units are reconciled — changed units are updated, units for new chart
files are created, and units whose chart file disappeared are deleted.

With no flags, upgrade is a plain re-render, which is how a hand-edit of the
HelmSource unit is applied.

Changes reach deployments only via 'cub variant promote' (preview with
--dry-run -o mutations).

Examples:
` + "```" + `
  # Upgrade to a new chart version
  cub helm upgrade cubbychat --version 1.3.0

  # Change values (replaces the stored values)
  cub helm upgrade cubbychat -f values.yaml --set ai.enabled=true

  # Re-render after editing the HelmSource unit by hand
  cub helm upgrade cubbychat
` + "```" + `
`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       ensureClient,
		RunE: func(cmd *cobra.Command, positional []string) error {
			releaseName := positional[0]

			component := args.component
			if component == "" {
				component = makeSlug(releaseName)
			} else {
				component = makeSlug(component)
			}

			spaces, err := getComponentSpaces(component)
			if err != nil {
				return err
			}

			sourceUnit, err := cub.UnitBySlug(spaces.source.SpaceID, makeSlug(releaseName))
			if err != nil {
				return err
			}
			if sourceUnit == nil {
				return fmt.Errorf("release %q is not installed in component %q; run 'cub helm install' first", releaseName, component)
			}
			data, err := base64.StdEncoding.DecodeString(sourceUnit.Data)
			if err != nil {
				return fmt.Errorf("failed to decode HelmSource unit %q: %w", sourceUnit.Slug, err)
			}
			src, err := helmutils.ParseHelmSource(data)
			if err != nil {
				return err
			}

			// Patch the HelmSource from the flags that were given.
			if cmd.Flags().Changed("version") {
				src.Spec.Chart.Version = args.version
			}
			if cmd.Flags().Changed("repo") {
				src.Spec.Chart.Repo = args.repo
			}
			if cmd.Flags().Changed("namespace") {
				src.Spec.Release.Namespace = args.namespace
			}
			if cmd.Flags().Changed("create-namespace") {
				src.Spec.CreateNamespace = args.createNamespace
			}
			if cmd.Flags().Changed("include-hooks") {
				src.Spec.IncludeHooks = args.includeHooks
			}
			if cmd.Flags().Changed("skip-crds") {
				src.Spec.SkipCRDs = args.skipCRDs
			}
			if len(args.valuesFiles) > 0 || len(args.set) > 0 {
				values, err := helmutils.MergeValues(args.valuesFiles, args.set)
				if err != nil {
					return err
				}
				src.Spec.Values = values
			}

			return applyHelmSource(src, component, spaces)
		},
	}

	f := cmd.Flags()
	f.StringVar(&args.component, "component", "", "component the release belongs to (defaults to the release name)")
	f.StringVar(&args.namespace, "namespace", "", "change the release namespace")
	f.BoolVar(&args.createNamespace, "create-namespace", false, "change whether a Namespace unit is synthesized")
	f.StringArrayVarP(&args.valuesFiles, "values", "f", []string{}, "specify values in a YAML file (can specify multiple); replaces the stored values")
	f.StringArrayVar(&args.set, "set", []string{}, "set values on the command line; replaces the stored values together with -f")
	f.StringVar(&args.version, "version", "", "change the chart version constraint")
	f.StringVar(&args.repo, "repo", "", "change the chart repository URL")
	f.BoolVar(&args.includeHooks, "include-hooks", false, "change whether helm.sh/hook manifests are kept as plain resources")
	f.BoolVar(&args.skipCRDs, "skip-crds", false, "change whether crds/ directories are skipped")
	f.BoolVar(&wait, "wait", true, "wait for triggers to finish on each written unit")
	f.BoolVar(&quiet, "quiet", false, "no per-unit output")

	return cmd
}

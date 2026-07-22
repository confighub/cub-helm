package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighub/sdk/bridge-impl/helmutils"
)

func newTemplateCmd() *cobra.Command {
	var args struct {
		prefix          string
		namespace       string
		createNamespace bool
		valuesFiles     []string
		set             []string
		version         string
		repo            string
		includeHooks    bool
		skipCRDs        bool
		outputDir       string
	}

	cmd := &cobra.Command{
		Use:   "template <release-name> <chart-ref>",
		Short: "Render a Helm chart locally, previewing the generated units",
		Long: `Render a Helm chart client-side and show the units 'cub helm install'
would generate, without requiring a ConfigHub server connection.

By default the rendered units are written to stdout, each preceded by a
"# Unit: <slug>" comment. Use --output-dir to write one <slug>.yaml file per
unit instead.

Examples:
` + "```" + `
  # Preview to stdout
  cub helm template cubbychat oci://ghcr.io/confighub/charts/cubbychat

  # Write one file per unit
  cub helm template cubbychat ./charts/cubbychat --output-dir ./out
` + "```" + `
`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, positional []string) error {
			releaseName := positional[0]
			chartRef := positional[1]

			values, err := helmutils.MergeValues(args.valuesFiles, args.set)
			if err != nil {
				return err
			}

			src := &helmutils.HelmSource{
				APIVersion: helmutils.HelmSourceAPIVersion,
				Kind:       helmutils.HelmSourceKind,
				Metadata:   helmutils.HelmSourceMetadata{Name: releaseName},
				Spec: helmutils.HelmSourceSpec{
					Chart: helmutils.HelmSourceChart{
						Ref:     chartRef,
						Repo:    args.repo,
						Version: args.version,
					},
					Release: helmutils.HelmSourceRelease{
						Name:      releaseName,
						Namespace: args.namespace,
					},
					CreateNamespace: args.createNamespace,
					UnitPrefix:      args.prefix,
					IncludeHooks:    args.includeHooks,
					SkipCRDs:        args.skipCRDs,
					Values:          values,
				},
			}

			chrt, err := helmutils.LoadChart(src)
			if err != nil {
				return err
			}
			result, err := helmutils.Generate(chrt, src, makeSlug(releaseName))
			if err != nil {
				return err
			}

			for _, dropped := range result.DroppedHooks {
				fmt.Fprintf(os.Stderr, "Dropped hook manifest: %s\n", dropped)
			}

			if args.outputDir != "" {
				if err := os.MkdirAll(args.outputDir, 0o755); err != nil {
					return fmt.Errorf("failed to create output directory %s: %w", args.outputDir, err)
				}
				for _, u := range result.Units {
					path := filepath.Join(args.outputDir, u.Slug+".yaml")
					if err := os.WriteFile(path, []byte(u.Content), 0o644); err != nil {
						return fmt.Errorf("failed to write %s: %w", path, err)
					}
					fmt.Fprintf(os.Stderr, "Wrote %s\n", path)
				}
				return nil
			}

			for i, u := range result.Units {
				if i > 0 {
					fmt.Print("---\n")
				}
				fmt.Printf("# Unit: %s\n", u.Slug)
				fmt.Print(u.Content)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&args.prefix, "prefix", "", "prefix for generated unit slugs")
	f.StringVar(&args.namespace, "namespace", "", "release namespace; when omitted the placeholder namespace is rendered")
	f.BoolVar(&args.createNamespace, "create-namespace", false, "synthesize a Namespace unit for the release namespace")
	f.StringArrayVarP(&args.valuesFiles, "values", "f", []string{}, "specify values in a YAML file (can specify multiple)")
	f.StringArrayVar(&args.set, "set", []string{}, "set values on the command line (can specify multiple or separate values with commas: key1=val1,key2=val2)")
	f.StringVar(&args.version, "version", "", "chart version constraint: a specific version (e.g. 1.1.1) or a range (e.g. ^2.0.0)")
	f.StringVar(&args.repo, "repo", "", "chart repository URL to resolve a bare chart name against")
	f.BoolVar(&args.includeHooks, "include-hooks", false, "keep helm.sh/hook manifests as plain resources instead of dropping them")
	f.BoolVar(&args.skipCRDs, "skip-crds", false, "do not generate units from the chart's crds/ directories")
	f.StringVar(&args.outputDir, "output-dir", "", "write one <slug>.yaml file per unit to this directory instead of stdout")

	return cmd
}

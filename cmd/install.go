package cmd

import (
	"github.com/spf13/cobra"

	"github.com/confighub/sdk/bridge-impl/helmutils"
)

func newInstallCmd() *cobra.Command {
	var args struct {
		component       string
		prefix          string
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
		Use:   "install <release-name> <chart-ref>",
		Short: "Install a Helm chart as a ConfigHub component",
		Long: `Render a Helm chart client-side and install it as a ConfigHub component.

The chart reference may be an oci:// reference, a local chart directory, or a
chart name resolved against --repo.

Install creates two spaces (if missing): the base variant space
<component>-base holding the rendered units, and the helm source space
<component>-helm holding one HelmSource unit per release with the chart
reference, values, and options. The component defaults to the release name.

Rendered output becomes one unit per chart template file, named from the
chart's file layout: templates/backend.yaml becomes unit "backend",
templates/rbac/role.yaml becomes "rbac-role", crds/foo.yaml becomes
"crds-foo", and subchart files are prefixed with the subchart name. When a
component contains multiple releases, each release's units are namespaced
with --prefix (defaulted to the release name for the second and later
releases).

The base is untargeted. If --namespace is not given, the release renders with
the confighubplaceholder namespace, which each deployment fills:

  cub variant create <variant> <component>-base --target <space>/<target> --namespace <ns>

Hook manifests are dropped by default because Helm's hook lifecycle cannot run
without Helm; --include-hooks keeps them as plain resources. The lookup
template function returns nothing and capabilities are Helm's defaults —
charts that depend on cluster access are out of scope.

Examples:
` + "```" + `
  # Install a chart as component "cubbychat" (spaces cubbychat-helm and cubbychat-base)
  cub helm install cubbychat oci://ghcr.io/confighub/charts/cubbychat

  # Add a second chart to the same component; its units are prefixed "pg-"
  cub helm install --component cubbychat --prefix pg pg oci://registry-1.docker.io/bitnamicharts/postgresql

  # Explicit namespace and a synthesized Namespace unit
  cub helm install --namespace cert-manager --create-namespace cert-manager jetstack/cert-manager --version v1.17.1
` + "```" + `
`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       ensureClient,
		RunE: func(cmd *cobra.Command, positional []string) error {
			releaseName := positional[0]
			chartRef := positional[1]

			component := args.component
			if component == "" {
				component = makeSlug(releaseName)
			} else {
				component = makeSlug(component)
			}

			values, err := helmutils.MergeValues(args.valuesFiles, args.set)
			if err != nil {
				return err
			}

			spaces, err := ensureComponentSpaces(component)
			if err != nil {
				return err
			}

			others, err := listHelmSources(spaces.source.SpaceID)
			if err != nil {
				return err
			}

			// The prefix defaults to empty for the component's first release
			// and to the release name for subsequent releases.
			prefix := args.prefix
			if !cmd.Flags().Changed("prefix") && countOtherReleases(others, releaseName) > 0 {
				prefix = makeSlug(releaseName)
			}
			if err := checkPrefixConflict(others, releaseName, prefix); err != nil {
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
					UnitPrefix:      prefix,
					IncludeHooks:    args.includeHooks,
					SkipCRDs:        args.skipCRDs,
					Values:          values,
				},
			}

			return applyHelmSource(src, component, spaces)
		},
	}

	f := cmd.Flags()
	f.StringVar(&args.component, "component", "", "component to install into (defaults to the release name); spaces <component>-helm and <component>-base are created if missing")
	f.StringVar(&args.prefix, "prefix", "", "prefix for generated unit slugs; required to be unique per release within a component, and empty for at most one release")
	f.StringVar(&args.namespace, "namespace", "", "release namespace, recorded and rendered literally; when omitted the placeholder namespace is used and deployments set it via 'cub variant create --namespace'")
	f.BoolVar(&args.createNamespace, "create-namespace", false, "synthesize a Namespace unit for the release namespace (skipped when the chart renders one itself)")
	f.StringArrayVarP(&args.valuesFiles, "values", "f", []string{}, "specify values in a YAML file (can specify multiple)")
	f.StringArrayVar(&args.set, "set", []string{}, "set values on the command line (can specify multiple or separate values with commas: key1=val1,key2=val2)")
	f.StringVar(&args.version, "version", "", "chart version constraint: a specific version (e.g. 1.1.1) or a range (e.g. ^2.0.0)")
	f.StringVar(&args.repo, "repo", "", "chart repository URL to resolve a bare chart name against")
	f.BoolVar(&args.includeHooks, "include-hooks", false, "keep helm.sh/hook manifests as plain resources instead of dropping them")
	f.BoolVar(&args.skipCRDs, "skip-crds", false, "do not generate units from the chart's crds/ directories (mirrors 'helm install --skip-crds')")
	f.BoolVar(&wait, "wait", true, "wait for triggers to finish on each written unit")
	f.BoolVar(&quiet, "quiet", false, "no per-unit output")

	return cmd
}

// countOtherReleases counts HelmSources other than the given release.
func countOtherReleases(sources []helmSourceUnit, releaseName string) int {
	count := 0
	for _, s := range sources {
		if s.unit.Slug != makeSlug(releaseName) {
			count++
		}
	}
	return count
}

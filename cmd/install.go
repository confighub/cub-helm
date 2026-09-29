package cmd

import (
	"github.com/spf13/cobra"

	"github.com/confighub/cub-helm/internal/helmrender"
)

// installArgs are the flags install and template share.
type installArgs struct {
	component       string
	namespace       string
	createNamespace bool
	valuesFiles     []string
	set             []string
	version         string
	repo            string
	includeHooks    bool
	skipCRDs        bool
	dryRun          bool
}

func newInstallCmd() *cobra.Command {
	var args installArgs

	cmd := &cobra.Command{
		Use:   "install <release-name> <chart-ref>",
		Short: "Install a Helm chart as a ConfigHub component",
		Long: `Render a Helm chart client-side and install it as a ConfigHub component.

The chart reference may be an oci:// reference, a local chart directory, or a
chart name resolved against --repo.

The rendered chart is uploaded into the component's base variant space,
<component>-base, which the upload creates if missing. The helm source space
<component>-helm holds one HelmSource unit per release with the chart
reference, values, and options. The component defaults to the release name.

Every rendered resource becomes its own unit. A workload's unit is named after
it, and any other resource's after its name and kind, with the component name
dropped from the front: in component "cubbychat", Deployment cubbychat-backend
becomes unit "backend" and its Service "backend-service". Each unit records the
chart template it came from in its UploadFile annotation. A component can hold
several releases; each owns the units it wrote, so installing or upgrading one
never changes another's.

The chart is rendered into its release namespace, --namespace, which defaults to
the release name. Charts write .Release.Namespace into places set-namespace
cannot rewrite, so the base carries a real namespace rather than a placeholder.
To run the chart in another namespace, install it under another component with
that --namespace.

Hook manifests are dropped by default because Helm's hook lifecycle cannot run
without Helm; --include-hooks keeps them as plain resources. The lookup
template function returns nothing and capabilities are Helm's defaults —
charts that depend on cluster access are out of scope.

Examples:
` + "```" + `
  # Install a chart as component "cubbychat" (spaces cubbychat-helm and cubbychat-base)
  cub helm install cubbychat oci://ghcr.io/confighub/charts/cubbychat

  # Add a second chart to the same component
  cub helm install --component cubbychat --namespace cubbychat pg oci://registry-1.docker.io/bitnamicharts/postgresql

  # Explicit namespace and a synthesized Namespace unit
  cub helm install --namespace cert-manager --create-namespace cert-manager jetstack/cert-manager --version v1.17.1
` + "```" + `
`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       ensureClient,
		RunE: func(cmd *cobra.Command, positional []string) error {
			return runInstall(&args, positional[0], positional[1], args.dryRun)
		},
	}

	addInstallFlags(cmd, &args)
	cmd.Flags().BoolVar(&args.dryRun, "dry-run", false, "report the units the upload would create, update, or empty, and write nothing")
	return cmd
}

// addInstallFlags registers the flags install and template share.
func addInstallFlags(cmd *cobra.Command, args *installArgs) {
	f := cmd.Flags()
	f.StringVar(&args.component, "component", "", "component to install into (defaults to the release name); spaces <component>-helm and <component>-base are created if missing")
	f.StringVar(&args.namespace, "namespace", "", "release namespace the chart is rendered into (defaults to the release name)")
	f.BoolVar(&args.createNamespace, "create-namespace", false, "synthesize a Namespace unit for the release namespace (skipped when the chart renders one itself)")
	f.StringArrayVarP(&args.valuesFiles, "values", "f", []string{}, "specify values in a YAML file (can specify multiple)")
	f.StringArrayVar(&args.set, "set", []string{}, "set values on the command line (can specify multiple or separate values with commas: key1=val1,key2=val2)")
	f.StringVar(&args.version, "version", "", "chart version constraint: a specific version (e.g. 1.1.1) or a range (e.g. ^2.0.0)")
	f.StringVar(&args.repo, "repo", "", "chart repository URL to resolve a bare chart name against")
	f.BoolVar(&args.includeHooks, "include-hooks", false, "keep helm.sh/hook manifests as plain resources instead of dropping them")
	f.BoolVar(&args.skipCRDs, "skip-crds", false, "do not upload the chart's crds/ directories (mirrors 'helm install --skip-crds')")
	f.BoolVar(&quiet, "quiet", false, "no per-unit output")
}

// runInstall renders a chart as a new or re-installed release and uploads it.
func runInstall(args *installArgs, releaseName, chartRef string, dryRun bool) error {
	component := makeSlug(releaseName)
	if args.component != "" {
		component = makeSlug(args.component)
	}

	values, err := helmrender.MergeValues(args.valuesFiles, args.set)
	if err != nil {
		return err
	}

	namespace := args.namespace
	if namespace == "" {
		namespace = releaseName
	}

	src := &helmrender.HelmSource{
		APIVersion: helmrender.HelmSourceAPIVersion,
		Kind:       helmrender.HelmSourceKind,
		Metadata:   helmrender.HelmSourceMetadata{Name: releaseName},
		Spec: helmrender.HelmSourceSpec{
			Chart: helmrender.HelmSourceChart{
				Ref:     chartRef,
				Repo:    args.repo,
				Version: args.version,
			},
			Release: helmrender.HelmSourceRelease{
				Name:      releaseName,
				Namespace: namespace,
			},
			CreateNamespace: args.createNamespace,
			IncludeHooks:    args.includeHooks,
			SkipCRDs:        args.skipCRDs,
			Values:          values,
		},
	}

	return applyHelmSource(src, component, dryRun)
}

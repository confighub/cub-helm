// Package helmrender renders a Helm chart on the client, as `helm template`
// does, into the files an upload sends: one per chart template, keyed by its
// path in the chart. It never contacts a cluster: lookup returns nothing and
// capabilities are Helm's defaults.
package helmrender

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// The labels stamped on every Unit a release writes, tracing it back to the chart.
const (
	HelmChartLabel           = "HelmChart"           // Chart.yaml name
	HelmReleaseLabel         = "HelmRelease"         // the release name
	HelmChartAPIVersionLabel = "HelmChartAPIVersion" // Chart.yaml apiVersion, e.g. v2
	HelmChartVersionLabel    = "HelmChartVersion"    // Chart.yaml version
	HelmAppVersionLabel      = "HelmAppVersion"      // Chart.yaml appVersion
)

// File is one rendered chart template.
type File struct {
	// Path is the template's path in the chart, e.g.
	// cubbychat/charts/postgresql/templates/primary/statefulset.yaml.
	Path    string
	Content string
}

// Result is the rendered output of one HelmSource.
type Result struct {
	Files []File
	// UnitLabels are the chart metadata labels to set on every Unit.
	UnitLabels map[string]string
	// ResolvedVersion is the concrete chart version that was rendered.
	ResolvedVersion string
	AppVersion      string
	// DroppedHooks describes hook manifests left out of the output, for reporting.
	DroppedHooks []string
	// SkippedCRDFiles lists crds/ files left out because of spec.skipCRDs.
	SkippedCRDFiles []string
}

// LoadChart locates and loads the chart a HelmSource refers to: an oci://
// reference, a local path, or a chart name resolved against spec.chart.repo.
// The version constraint from spec.chart.version is applied during resolution.
func LoadChart(src *HelmSource) (*chart.Chart, error) {
	settings := cli.New()
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(nil, "", os.Getenv("HELM_DRIVER"), func(format string, v ...any) {}); err != nil {
		return nil, fmt.Errorf("failed to initialize Helm action configuration: %w", err)
	}

	registryClient, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize OCI registry client: %w", err)
	}
	actionConfig.RegistryClient = registryClient

	// action.NewInstall wires the registry client into ChartPathOptions.
	installAction := action.NewInstall(actionConfig)
	installAction.ChartPathOptions.Version = src.Spec.Chart.Version
	installAction.ChartPathOptions.RepoURL = src.Spec.Chart.Repo

	cp, err := installAction.ChartPathOptions.LocateChart(src.Spec.Chart.Ref, settings)
	if err != nil {
		return nil, fmt.Errorf("failed to locate chart %s (version: %s, repo: %s): %w",
			src.Spec.Chart.Ref, src.Spec.Chart.Version, src.Spec.Chart.Repo, err)
	}

	chrt, err := loader.Load(cp)
	if err != nil {
		return nil, fmt.Errorf("failed to load chart from %s: %w", cp, err)
	}
	return chrt, nil
}

// Render renders the chart per the HelmSource into one File per template that
// produced any output, plus one per crds/ file. Each document is preceded by the
// "# Source: <path>" comment `helm template` writes, which is what the upload
// records as the resource's file. Hook manifests are dropped unless
// spec.includeHooks is set. The release Namespace is not synthesized here: the
// upload does that when spec.createNamespace is set.
func Render(chrt *chart.Chart, src *HelmSource) (*Result, error) {
	values := src.Spec.Values
	if values == nil {
		values = map[string]any{}
	}
	if err := chartutil.ProcessDependencies(chrt, values); err != nil {
		return nil, fmt.Errorf("failed to process chart dependencies: %w", err)
	}

	releaseOptions := chartutil.ReleaseOptions{
		Name:      src.Spec.Release.Name,
		Namespace: src.RenderNamespace(),
		Revision:  1,
		IsInstall: true,
	}
	valuesToRender, err := chartutil.ToRenderValues(chrt, values, releaseOptions, chartutil.DefaultCapabilities)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare render values: %w", err)
	}

	renderedFiles, err := engine.Engine{}.Render(chrt, valuesToRender)
	if err != nil {
		return nil, fmt.Errorf("template render failed: %w", err)
	}

	result := &Result{
		UnitLabels:      unitLabels(chrt, src.Spec.Release.Name),
		ResolvedVersion: chrt.Metadata.Version,
		AppVersion:      chrt.Metadata.AppVersion,
	}

	paths := make([]string, 0, len(renderedFiles))
	for p := range renderedFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		base := path.Base(p)
		if strings.HasPrefix(base, "_") || base == "NOTES.txt" || strings.TrimSpace(renderedFiles[p]) == "" {
			continue
		}
		content, err := manifestsOf(p, renderedFiles[p], src.Spec.IncludeHooks, result)
		if err != nil {
			return nil, err
		}
		if content != "" {
			result.Files = append(result.Files, File{Path: p, Content: content})
		}
	}

	// CRDs from crds/ directories, including dependency charts'. They are not
	// templated, so the file is used as it is.
	for _, crd := range chrt.CRDObjects() {
		if src.Spec.SkipCRDs {
			result.SkippedCRDFiles = append(result.SkippedCRDFiles, crd.Filename)
			continue
		}
		result.Files = append(result.Files, File{
			Path:    crd.Filename,
			Content: fmt.Sprintf("---\n# Source: %s\n%s\n", crd.Filename, strings.TrimSpace(string(crd.File.Data))),
		})
	}
	return result, nil
}

// manifestsOf writes one rendered template's documents as `helm template` does,
// each after a "# Source:" comment, leaving out hook manifests unless they are
// included. Documents are split with Helm's own splitter and written as rendered,
// so nothing about them is re-serialized.
func manifestsOf(templatePath, rendered string, includeHooks bool, result *Result) (string, error) {
	docs := releaseutil.SplitManifests(rendered)
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Sort(releaseutil.BySplitManifestsOrder(keys))

	var b strings.Builder
	for _, k := range keys {
		doc := docs[k]
		var head releaseutil.SimpleHead
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			return "", fmt.Errorf("YAML parse error on %s: %w", templatePath, err)
		}
		if head.Metadata != nil {
			if hook, ok := head.Metadata.Annotations[release.HookAnnotation]; ok && !includeHooks {
				result.DroppedHooks = append(result.DroppedHooks, fmt.Sprintf("%s %s (%s: %s) from %s",
					head.Kind, head.Metadata.Name, release.HookAnnotation, hook, templatePath))
				continue
			}
		}
		fmt.Fprintf(&b, "---\n# Source: %s\n%s\n", templatePath, doc)
	}
	return b.String(), nil
}

// unitLabels builds the chart metadata labels set on every Unit the release writes.
func unitLabels(chrt *chart.Chart, releaseName string) map[string]string {
	labels := map[string]string{HelmReleaseLabel: releaseName}
	if m := chrt.Metadata; m != nil {
		for label, value := range map[string]string{
			HelmChartLabel:           m.Name,
			HelmChartAPIVersionLabel: m.APIVersion,
			HelmChartVersionLabel:    m.Version,
			HelmAppVersionLabel:      m.AppVersion,
		} {
			if value != "" {
				labels[label] = value
			}
		}
	}
	return labels
}

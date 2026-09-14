package helmrender

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart"
)

func testChart() *chart.Chart {
	return &chart.Chart{
		Metadata: &chart.Metadata{
			Name:       "testchart",
			APIVersion: "v2",
			Version:    "1.2.3",
			AppVersion: "0.9.0",
		},
		Templates: []*chart.File{
			{Name: "templates/deployment.yaml", Data: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-app
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.replicas | default 1 }}
`)},
			{Name: "templates/rbac.yaml", Data: []byte(`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: app-role
---
apiVersion: batch/v1
kind: Job
metadata:
  name: setup-hook
  annotations:
    helm.sh/hook: pre-install
`)},
			{Name: "templates/empty.yaml", Data: []byte(`{{- if .Values.never }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: never
{{- end }}
`)},
			{Name: "templates/_helpers.tpl", Data: []byte(`{{- define "noop" -}}{{- end -}}`)},
			{Name: "templates/NOTES.txt", Data: []byte(`installed!`)},
		},
		Files: []*chart.File{
			{Name: "crds/widgets.yaml", Data: []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
`)},
		},
	}
}

func testSource() *HelmSource {
	return &HelmSource{
		APIVersion: HelmSourceAPIVersion,
		Kind:       HelmSourceKind,
		Metadata:   HelmSourceMetadata{Name: "myrelease"},
		Spec: HelmSourceSpec{
			Chart:   HelmSourceChart{Ref: "testchart", Version: "1.2.3"},
			Release: HelmSourceRelease{Name: "myrelease"},
		},
	}
}

func filesByPath(result *Result) map[string]string {
	files := map[string]string{}
	for _, f := range result.Files {
		files[f.Path] = f.Content
	}
	return files
}

// Each template that renders anything is one file keyed by its chart path, with
// the "# Source:" comment helm template writes; partials, NOTES.txt and templates
// that render nothing are left out.
func TestRenderOneFilePerTemplate(t *testing.T) {
	result, err := Render(testChart(), testSource())
	require.NoError(t, err)

	files := filesByPath(result)
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	assert.ElementsMatch(t, []string{
		"testchart/templates/deployment.yaml",
		"testchart/templates/rbac.yaml",
		"testchart/crds/widgets.yaml",
	}, paths)
	assert.True(t, strings.HasPrefix(files["testchart/templates/deployment.yaml"],
		"---\n# Source: testchart/templates/deployment.yaml\napiVersion: apps/v1\n"))
	assert.True(t, strings.HasPrefix(files["testchart/crds/widgets.yaml"],
		"---\n# Source: testchart/crds/widgets.yaml\napiVersion: apiextensions.k8s.io/v1\n"))

	assert.Equal(t, map[string]string{
		HelmReleaseLabel:         "myrelease",
		HelmChartLabel:           "testchart",
		HelmChartAPIVersionLabel: "v2",
		HelmChartVersionLabel:    "1.2.3",
		HelmAppVersionLabel:      "0.9.0",
	}, result.UnitLabels)
	assert.Equal(t, "1.2.3", result.ResolvedVersion)
	assert.Equal(t, "0.9.0", result.AppVersion)
}

// Without a release namespace the chart renders into one named after the release.
func TestRenderNamespaceDefaultsToReleaseName(t *testing.T) {
	result, err := Render(testChart(), testSource())
	require.NoError(t, err)
	assert.Contains(t, filesByPath(result)["testchart/templates/deployment.yaml"], "namespace: myrelease")

	src := testSource()
	src.Spec.Release.Namespace = "apps"
	result, err = Render(testChart(), src)
	require.NoError(t, err)
	assert.Contains(t, filesByPath(result)["testchart/templates/deployment.yaml"], "namespace: apps")
}

// A hook manifest is dropped from its template's file, and the rest of the file kept.
func TestRenderDropsHooksByDefault(t *testing.T) {
	result, err := Render(testChart(), testSource())
	require.NoError(t, err)
	rbac := filesByPath(result)["testchart/templates/rbac.yaml"]
	assert.Contains(t, rbac, "kind: Role")
	assert.NotContains(t, rbac, "setup-hook")
	require.Len(t, result.DroppedHooks, 1)
	assert.Contains(t, result.DroppedHooks[0], "Job setup-hook")
	assert.Contains(t, result.DroppedHooks[0], "testchart/templates/rbac.yaml")
}

func TestRenderIncludeHooks(t *testing.T) {
	src := testSource()
	src.Spec.IncludeHooks = true
	result, err := Render(testChart(), src)
	require.NoError(t, err)
	rbac := filesByPath(result)["testchart/templates/rbac.yaml"]
	assert.Contains(t, rbac, "kind: Role")
	assert.Contains(t, rbac, "setup-hook")
	assert.Equal(t, 2, strings.Count(rbac, "# Source: testchart/templates/rbac.yaml"))
	assert.Empty(t, result.DroppedHooks)
}

func TestRenderSkipCRDs(t *testing.T) {
	src := testSource()
	src.Spec.SkipCRDs = true
	result, err := Render(testChart(), src)
	require.NoError(t, err)
	assert.NotContains(t, filesByPath(result), "testchart/crds/widgets.yaml")
	assert.Equal(t, []string{"testchart/crds/widgets.yaml"}, result.SkippedCRDFiles)
}

func TestRenderValues(t *testing.T) {
	src := testSource()
	src.Spec.Values = map[string]any{"replicas": 3}
	result, err := Render(testChart(), src)
	require.NoError(t, err)
	assert.Contains(t, filesByPath(result)["testchart/templates/deployment.yaml"], "replicas: 3")
}

// A subchart's templates and CRDs are keyed under the parent chart's path.
func TestRenderSubchart(t *testing.T) {
	parent := testChart()
	sub := &chart.Chart{
		Metadata: &chart.Metadata{Name: "db", APIVersion: "v2", Version: "0.1.0"},
		Templates: []*chart.File{
			{Name: "templates/statefulset.yaml", Data: []byte("apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: {{ .Release.Name }}-db\n")},
		},
	}
	parent.AddDependency(sub)
	result, err := Render(parent, testSource())
	require.NoError(t, err)
	assert.Contains(t, filesByPath(result)["testchart/charts/db/templates/statefulset.yaml"], "name: myrelease-db")
}

func TestParseHelmSourceRoundTrip(t *testing.T) {
	src := testSource()
	src.Spec.Values = map[string]any{"replicas": 2, "image": map[string]any{"tag": "v1"}}
	src.Status.ResolvedVersion = "1.2.3"

	data, err := src.Marshal()
	require.NoError(t, err)

	parsed, err := ParseHelmSource(data)
	require.NoError(t, err)
	// YAML numbers decode as float64, so compare the serialized forms.
	reData, err := parsed.Marshal()
	require.NoError(t, err)
	assert.Equal(t, string(data), string(reData))
	assert.Equal(t, "1.2.3", parsed.Status.ResolvedVersion)
}

func TestParseHelmSourceValidation(t *testing.T) {
	_, err := ParseHelmSource([]byte("apiVersion: v1\nkind: ConfigMap\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a HelmSource")

	_, err = ParseHelmSource([]byte("apiVersion: confighub.com/v1alpha1\nkind: HelmSource\nmetadata:\n  name: x\nspec:\n  chart:\n    ref: oci://example/chart\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.release.name")
}

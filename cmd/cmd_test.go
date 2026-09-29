package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/confighub/cub-helm/internal/helmrender"
)

func TestRootHasAllSubcommands(t *testing.T) {
	r := NewRootCmd()
	wantCmds := []string{"install", "upgrade", "template", "version"}
	for _, want := range wantCmds {
		found := false
		for _, sc := range r.Commands() {
			if sc.Name() == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing subcommand %q", want)
		}
	}
}

func TestVersionPrints(t *testing.T) {
	r := NewRootCmd()
	var buf bytes.Buffer
	r.SetOut(&buf)
	r.SetArgs([]string{"version"})
	if err := r.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "helm ") {
		t.Errorf("expected version output, got %q", buf.String())
	}
}

func TestMakeSlug(t *testing.T) {
	cases := map[string]string{
		"cubbychat":    "cubbychat",
		"cert-manager": "cert-manager",
	}
	for in, want := range cases {
		if got := makeSlug(in); got != want {
			t.Errorf("makeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// A release's upload names the component's base and owns its Units by release
// name.
func TestUploadRequest(t *testing.T) {
	src := &helmrender.HelmSource{
		Spec: helmrender.HelmSourceSpec{
			Chart:           helmrender.HelmSourceChart{Ref: "oci://example.com/charts/pg"},
			Release:         helmrender.HelmSourceRelease{Name: "pg"},
			CreateNamespace: true,
		},
	}
	result := &helmrender.Result{
		Files: []helmrender.File{
			{Path: "pg/templates/statefulset.yaml", Content: "---\n# Source: pg/templates/statefulset.yaml\n"},
			{Path: "pg/templates/config.tpl", Content: "---\n# Source: pg/templates/config.tpl\n"},
		},
		UnitLabels:      map[string]string{helmrender.HelmChartLabel: "postgresql", helmrender.HelmReleaseLabel: "pg"},
		ResolvedVersion: "16.2.0",
	}

	req := uploadRequest(src, "cubbychat", result)
	if len(req.Components) != 1 {
		t.Fatalf("expected one component, got %d", len(req.Components))
	}
	c := req.Components[0]
	if c.Name != "cubbychat" || c.Space != "cubbychat-base" || c.SourceName != "pg" {
		t.Errorf("component = %+v, want cubbychat in cubbychat-base owned by pg", c)
	}
	if c.Namespace != "pg" {
		t.Errorf("namespace = %q, want the release name", c.Namespace)
	}
	if c.SlugPrefix != "" || !c.CreateNamespace {
		t.Errorf("SlugPrefix = %q, CreateNamespace = %v", c.SlugPrefix, c.CreateNamespace)
	}
	if req.SpaceLabels["Variant"] != "base" {
		t.Errorf("SpaceLabels = %v, want Variant=base", req.SpaceLabels)
	}
	if got := []string{req.Files[0].Path, req.Files[1].Path}; got[0] != "pg/templates/statefulset.yaml" || got[1] != "pg/templates/config.tpl.yaml" {
		t.Errorf("file paths = %v", got)
	}
	if !strings.Contains(req.ChangeDescription, "postgresql 16.2.0") {
		t.Errorf("ChangeDescription = %q", req.ChangeDescription)
	}
}

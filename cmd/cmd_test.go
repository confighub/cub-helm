package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// TestTemplateOffline renders a minimal local chart and checks that the
// template command works without a server connection.
func TestTemplateOffline(t *testing.T) {
	dir := t.TempDir()
	chart := filepath.Join(dir, "demo")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(chart, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(chart, "templates", "cm.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-cm\ndata:\n  hello: world\n")

	out := filepath.Join(dir, "out")
	r := NewRootCmd()
	r.SetArgs([]string{"template", "demo", chart, "--output-dir", out})
	if err := r.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(out, "cm.yaml"))
	if err != nil {
		t.Fatalf("expected rendered unit file: %v", err)
	}
	if !strings.Contains(string(got), "demo-cm") {
		t.Errorf("rendered unit missing release name substitution: %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

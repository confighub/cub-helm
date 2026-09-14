package cmd

import (
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"

	goclient "github.com/confighub/sdk/core/openapi/goclient-new"

	"github.com/confighub/cub-helm/internal/helmrender"
)

// helmSourceUnit pairs a source-space unit with its parsed HelmSource document.
type helmSourceUnit struct {
	unit   *goclient.Unit
	source *helmrender.HelmSource
}

// listHelmSources returns the parsed HelmSource units in the source space.
// Units that do not parse are skipped with a warning.
func listHelmSources(sourceSpaceID uuid.UUID) ([]helmSourceUnit, error) {
	units, err := cub.ListUnits(sourceSpaceID, "")
	if err != nil {
		return nil, err
	}
	content, err := cub.ListUnitData(sourceSpaceID, "")
	if err != nil {
		return nil, err
	}
	sources := make([]helmSourceUnit, 0, len(units))
	for _, u := range units {
		data, ok := content[u.UnitID]
		if !ok {
			continue
		}
		src, err := helmrender.ParseHelmSource([]byte(data))
		if err != nil {
			tprint("Warning: unit %s in the helm source space is not a valid HelmSource: %v", u.Slug, err)
			continue
		}
		sources = append(sources, helmSourceUnit{unit: u, source: src})
	}
	return sources, nil
}

// checkPrefixConflict enforces that no two HelmSources in a component share a
// unit prefix. In particular at most one may have an empty prefix.
func checkPrefixConflict(others []helmSourceUnit, release, prefix string) error {
	for _, other := range others {
		if other.unit.Slug == makeSlug(release) {
			continue
		}
		if other.source.Spec.UnitPrefix == prefix {
			if prefix == "" {
				return fmt.Errorf("release %q already uses an empty unit prefix in this component; pass --prefix", other.source.Spec.Release.Name)
			}
			return fmt.Errorf("release %q already uses unit prefix %q in this component; pass a different --prefix", other.source.Spec.Release.Name, prefix)
		}
	}
	return nil
}

// applyHelmSource renders the HelmSource and uploads the result into the
// component's base space, then records the HelmSource in the source space. It
// is the shared core of install, upgrade, and template. The upload is what
// decides which Units to create, merge, or empty; with dryRun it only reports
// that, and nothing is written.
func applyHelmSource(src *helmrender.HelmSource, component string, dryRun bool) error {
	chrt, err := helmrender.LoadChart(src)
	if err != nil {
		return err
	}

	result, err := helmrender.Render(chrt, src)
	if err != nil {
		return err
	}

	for _, dropped := range result.DroppedHooks {
		tprint("Dropped hook manifest: %s (use --include-hooks to keep hook manifests as plain resources)", dropped)
	}
	if len(result.SkippedCRDFiles) > 0 {
		tprint("Skipped %d CRD file(s) due to --skip-crds", len(result.SkippedCRDFiles))
	}
	if src.Spec.IncludeHooks && renderedHooks(result) {
		tprint("Note: hook manifests are included as plain resources; Helm hook lifecycle (weights, deletion policies) does not apply")
	}

	src.Status.ResolvedVersion = result.ResolvedVersion
	src.Status.AppVersion = result.AppVersion

	uploaded, err := cub.Upload(uploadRequest(src, component, result), dryRun)
	if err != nil {
		return fmt.Errorf("failed to upload release %q: %w", src.Spec.Release.Name, err)
	}
	reportUpload(uploaded)
	if dryRun {
		return nil
	}

	baseSpaceID, err := uploadedSpaceID(uploaded)
	if err != nil {
		return err
	}
	sourceSpace, err := ensureSourceSpace(component, baseSpaceID)
	if err != nil {
		return err
	}
	// The HelmSource records what was rendered even when some writes failed: the
	// ones that landed are real, and upgrade re-renders from it to finish the rest.
	if err := upsertHelmSourceUnit(sourceSpace.SpaceID, src, result.UnitLabels); err != nil {
		return err
	}
	return uploadFailures(uploaded)
}

// uploadRequest builds the upload of one release's rendered chart. The release
// owns its Units by name, so releases sharing a component's base never write or
// empty each other's Units.
func uploadRequest(src *helmrender.HelmSource, component string, result *helmrender.Result) goclient.UploadRequest {
	files := make([]goclient.UploadRequestFile, 0, len(result.Files))
	for _, f := range result.Files {
		files = append(files, goclient.UploadRequestFile{Path: uploadPath(f.Path), Content: f.Content})
	}
	slugPrefix := ""
	if src.Spec.UnitPrefix != "" {
		slugPrefix = src.Spec.UnitPrefix + "-"
	}
	chart := result.UnitLabels[helmrender.HelmChartLabel]
	return goclient.UploadRequest{
		Files: files,
		Source: &goclient.UploadSourceInfo{
			Ref:           src.Spec.Chart.Ref,
			Client:        "cub-helm",
			ClientVersion: version,
		},
		Components: []goclient.UploadComponentRequest{{
			Name:            component,
			SourceName:      makeSlug(src.Spec.Release.Name),
			Namespace:       src.RenderNamespace(),
			CreateNamespace: src.Spec.CreateNamespace,
			SlugPrefix:      slugPrefix,
			Space:           component + baseSpaceSuffix,
			UnitLabels:      result.UnitLabels,
		}},
		SpaceLabels:       map[string]string{"Variant": variantLabelBase},
		ChangeDescription: fmt.Sprintf("Rendered chart %s %s for Helm release %s", chart, result.ResolvedVersion, src.Spec.Release.Name),
	}
}

// uploadPath names a rendered template in the upload. The server reads only
// YAML and JSON files, so a template with another extension is sent with .yaml
// appended; its "# Source:" comment still records the template's own path.
func uploadPath(templatePath string) string {
	switch strings.ToLower(path.Ext(templatePath)) {
	case ".yaml", ".yml", ".json":
		return templatePath
	}
	return templatePath + ".yaml"
}

// renderedHooks reports whether the render produced any hook manifests. When
// hooks are included they are not in DroppedHooks, so detect them by scanning
// the rendered content for the annotation.
func renderedHooks(result *helmrender.Result) bool {
	if len(result.DroppedHooks) > 0 {
		return true
	}
	for _, f := range result.Files {
		if strings.Contains(f.Content, "helm.sh/hook:") || strings.Contains(f.Content, `"helm.sh/hook"`) {
			return true
		}
	}
	return false
}

// uploadedSpaceID returns the base space the upload wrote to.
func uploadedSpaceID(result *goclient.UploadResult) (uuid.UUID, error) {
	for _, c := range result.Components {
		for _, s := range c.Spaces {
			if s.SpaceID != nil {
				return *s.SpaceID, nil
			}
		}
	}
	return uuid.UUID{}, fmt.Errorf("the upload reported no base space")
}

// uploadFailures returns an error naming the Unit and Link writes that failed.
func uploadFailures(result *goclient.UploadResult) error {
	var failed []string
	for _, c := range result.Components {
		for _, s := range c.Spaces {
			for _, u := range s.Units {
				if u.Error != nil {
					failed = append(failed, "unit "+u.Slug)
				}
			}
			for _, l := range s.Links {
				if l.Error != nil {
					failed = append(failed, fmt.Sprintf("link %s -> %s", l.FromUnit, l.ToUnit))
				}
			}
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("%d write(s) failed: %s; run 'cub helm upgrade' to retry", len(failed), strings.Join(failed, ", "))
}

// reportUpload prints what the upload did, or would do.
func reportUpload(result *goclient.UploadResult) {
	if result.DryRun {
		tprint("Dry run: nothing was written.")
	}
	for _, c := range result.Components {
		for _, s := range c.Spaces {
			tprint("Space %s (%s)", s.SpaceSlug, s.Action)
			unchanged := 0
			for _, u := range s.Units {
				switch {
				case u.Error != nil:
					tprint("  %-9s %s: %s", "FAILED", u.Slug, errString(u.Error))
				case u.Action == "Unchanged":
					unchanged++
				case !quiet:
					tprint("  %-9s %s", u.Action, u.Slug)
				}
			}
			if unchanged > 0 && !quiet {
				tprint("  %-9s %d Unit(s)", "Unchanged", unchanged)
			}
			for _, l := range s.Links {
				switch {
				case l.Error != nil:
					tprint("  link FAILED %s -> %s: %s", l.FromUnit, l.ToUnit, errString(l.Error))
				case l.Action == "Create" && !quiet:
					tprint("  linked    %s -> %s (%s)", l.FromUnit, l.ToUnit, l.Reason)
				}
			}
		}
		if c.NamespaceCollision != nil {
			tprint("Note: --create-namespace was given, but the chart already renders Namespace %q, so none was synthesized.",
				c.NamespaceCollision.Namespace)
		}
		if len(c.SkippedSecrets) > 0 {
			tprint("Note: %d Secret(s) were NOT uploaded. Apply them out-of-band:", len(c.SkippedSecrets))
			for _, s := range c.SkippedSecrets {
				tprint("  - %s", s)
			}
		}
		if len(c.UnmatchedReferences) > 0 && !quiet {
			tprint("Note: these references didn't resolve to any resource in the chart (expected when the")
			tprint("resource is created elsewhere, such as a namespace or a Secret):")
			for _, u := range c.UnmatchedReferences {
				tprint("  - %s -> %s %q", u.FromUnit, u.TargetType, u.TargetName)
			}
		}
	}
}

// errString renders a per-item error from the upload response.
func errString(e *goclient.ResponseError) string {
	if e == nil || e.Message == "" {
		return "unknown error"
	}
	return e.Message
}

// upsertHelmSourceUnit creates or updates the HelmSource unit in the source space.
func upsertHelmSourceUnit(sourceSpaceID uuid.UUID, src *helmrender.HelmSource, labels map[string]string) error {
	data, err := src.Marshal()
	if err != nil {
		return err
	}
	slug := makeSlug(src.Spec.Release.Name)

	existing, err := cub.UnitBySlug(sourceSpaceID, slug)
	if err != nil {
		return err
	}
	if existing == nil {
		created, err := cub.CreateUnit(sourceSpaceID, goclient.Unit{
			SpaceID:       sourceSpaceID,
			Slug:          slug,
			ToolchainType: toolchainConfigHubYAML,
			Labels:        labels,
		})
		if err != nil {
			return fmt.Errorf("failed to create HelmSource unit %q: %w", slug, err)
		}
		if _, err := cub.PutUnitData(sourceSpaceID, created.UnitID, string(data)); err != nil {
			return fmt.Errorf("failed to write HelmSource unit %q: %w", slug, err)
		}
		tprint("Created HelmSource unit %s", slug)
		return nil
	}

	if !labelsMatch(existing.Labels, labels) {
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range labels {
			existing.Labels[k] = v
		}
		if _, err := cub.UpdateUnit(existing.SpaceID, existing); err != nil {
			return fmt.Errorf("failed to update HelmSource unit %q: %w", slug, err)
		}
	}
	updated, err := cub.PutUnitData(existing.SpaceID, existing.UnitID, string(data))
	if err != nil {
		return fmt.Errorf("failed to update HelmSource unit %q: %w", slug, err)
	}
	tprint("Updated HelmSource unit %s", updated.Slug)
	return nil
}

// labelsMatch reports whether every wanted label is present with the same value.
func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

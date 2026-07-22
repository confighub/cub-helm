package cmd

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/confighub/sdk/bridge-impl/helmutils"
	goclient "github.com/confighub/sdk/core/openapi/goclient-new"
)

// helmSourceUnit pairs a source-space unit with its parsed HelmSource document.
type helmSourceUnit struct {
	unit   *goclient.Unit
	source *helmutils.HelmSource
}

// listHelmSources returns the parsed HelmSource units in the source space.
// Units that do not parse are skipped with a warning.
func listHelmSources(sourceSpaceID uuid.UUID) ([]helmSourceUnit, error) {
	units, err := cub.ListUnits(sourceSpaceID, "")
	if err != nil {
		return nil, err
	}
	sources := make([]helmSourceUnit, 0, len(units))
	for _, u := range units {
		data, err := base64.StdEncoding.DecodeString(u.Data)
		if err != nil {
			continue
		}
		src, err := helmutils.ParseHelmSource(data)
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

// applyHelmSource renders the HelmSource and reconciles both the source unit
// and the generated units in the base space. It is the shared core of install
// and upgrade.
func applyHelmSource(src *helmutils.HelmSource, component string, spaces *helmComponentSpaces) error {
	chrt, err := helmutils.LoadChart(src)
	if err != nil {
		return err
	}

	result, err := helmutils.Generate(chrt, src, component)
	if err != nil {
		return err
	}

	for _, dropped := range result.DroppedHooks {
		tprint("Dropped hook manifest: %s (use --include-hooks to keep hook manifests as plain resources)", dropped)
	}
	if len(result.SkippedCRDFiles) > 0 {
		tprint("Skipped %d CRD file(s) due to --skip-crds", len(result.SkippedCRDFiles))
	}
	if src.Spec.IncludeHooks && chartDeclaresHooks(result) {
		tprint("Note: hook manifests are included as plain resources; Helm hook lifecycle (weights, deletion policies) does not apply")
	}

	src.Status.ResolvedVersion = result.ResolvedVersion
	src.Status.AppVersion = result.AppVersion

	if err := upsertHelmSourceUnit(spaces.source.SpaceID, src, result.UnitLabels); err != nil {
		return err
	}

	return reconcileHelmUnits(spaces.base.SpaceID, src, result)
}

// chartDeclaresHooks reports whether the render produced any hook manifests.
// When hooks are included they are not in DroppedHooks, so detect them by
// scanning the generated content for the annotation.
func chartDeclaresHooks(result *helmutils.GenerateResult) bool {
	if len(result.DroppedHooks) > 0 {
		return true
	}
	for _, u := range result.Units {
		if containsHelmHookAnnotation(u.Content) {
			return true
		}
	}
	return false
}

func containsHelmHookAnnotation(content string) bool {
	return strings.Contains(content, "helm.sh/hook:") || strings.Contains(content, `"helm.sh/hook"`)
}

// upsertHelmSourceUnit creates or updates the HelmSource unit in the source space.
func upsertHelmSourceUnit(sourceSpaceID uuid.UUID, src *helmutils.HelmSource, labels map[string]string) error {
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
		if _, err := createUnitInSpace(sourceSpaceID, slug, toolchainConfigHubYAML, string(data), labels); err != nil {
			return fmt.Errorf("failed to create HelmSource unit %q: %w", slug, err)
		}
		tprint("Created HelmSource unit %s", slug)
		return nil
	}

	existing.Data = base64.StdEncoding.EncodeToString(data)
	mergeLabels(existing, labels)
	updated, err := cub.UpdateUnit(existing.SpaceID, existing)
	if err != nil {
		return fmt.Errorf("failed to update HelmSource unit %q: %w", slug, err)
	}
	tprint("Updated HelmSource unit %s", updated.Slug)
	return nil
}

// reconcileHelmUnits makes the base space's units for this release match the
// generated set: changed units are updated, new ones created, and units whose
// source file disappeared are deleted.
func reconcileHelmUnits(baseSpaceID uuid.UUID, src *helmutils.HelmSource, result *helmutils.GenerateResult) error {
	release := src.Spec.Release.Name
	existing, err := cub.ListUnits(baseSpaceID, fmt.Sprintf("Labels.%s = '%s'", helmutils.HelmReleaseLabel, release))
	if err != nil {
		return err
	}
	existingBySlug := map[string]*goclient.Unit{}
	for _, u := range existing {
		existingBySlug[u.Slug] = u
	}

	desired := map[string]bool{}
	for _, gen := range result.Units {
		desired[gen.Slug] = true
		if ex, ok := existingBySlug[gen.Slug]; ok {
			current, decodeErr := base64.StdEncoding.DecodeString(ex.Data)
			if decodeErr == nil && string(current) == gen.Content && labelsMatch(ex.Labels, result.UnitLabels) {
				continue
			}
			ex.Data = base64.StdEncoding.EncodeToString([]byte(gen.Content))
			mergeLabels(ex, result.UnitLabels)
			updated, err := cub.UpdateUnit(ex.SpaceID, ex)
			if err != nil {
				return fmt.Errorf("failed to update unit %q: %w", gen.Slug, err)
			}
			if wait {
				if err := awaitTriggersRemoval(updated); err != nil {
					return err
				}
			}
			reportUnitUpdated(updated.Slug, updated.UnitID.String())
			continue
		}

		// A synthesized Namespace unit (Source == "") may already exist,
		// created by another release sharing the namespace; leave it alone.
		if gen.Source == "" {
			other, err := cub.UnitBySlug(baseSpaceID, gen.Slug)
			if err != nil {
				return err
			}
			if other != nil {
				if !quiet {
					tprint("Namespace unit %s already exists (shared); leaving it unchanged", gen.Slug)
				}
				continue
			}
		}

		created, err := createUnitInSpace(baseSpaceID, gen.Slug, toolchainKubernetesYAML, gen.Content, result.UnitLabels)
		if err != nil {
			return fmt.Errorf("failed to create unit %q: %w", gen.Slug, err)
		}
		if wait {
			if err := awaitTriggersRemoval(created); err != nil {
				return err
			}
		}
		reportUnitCreated(created.Slug, created.UnitID.String())
	}

	for slug, ex := range existingBySlug {
		if desired[slug] {
			continue
		}
		if err := cub.DeleteUnit(baseSpaceID, ex.UnitID); err != nil {
			return fmt.Errorf("failed to delete unit %q (its source file was removed from the chart): %w", slug, err)
		}
		tprint("Deleted unit %s (its source file was removed from the chart)", slug)
	}

	return nil
}

// createUnitInSpace creates a unit with the given content and labels.
func createUnitInSpace(spaceID uuid.UUID, slug, toolchainType, content string, labels map[string]string) (*goclient.Unit, error) {
	return cub.CreateUnit(spaceID, goclient.Unit{
		SpaceID:       spaceID,
		Slug:          slug,
		ToolchainType: toolchainType,
		Data:          base64.StdEncoding.EncodeToString([]byte(content)),
		Labels:        labels,
	})
}

// mergeLabels sets the given labels on the unit, preserving unrelated ones.
func mergeLabels(unit *goclient.Unit, labels map[string]string) {
	if unit.Labels == nil {
		unit.Labels = map[string]string{}
	}
	maps.Copy(unit.Labels, labels)
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

// reportUnitCreated / reportUnitUpdated print a quiet-aware confirmation line.
func reportUnitCreated(slug, id string) {
	if !quiet {
		tprint("Successfully created unit %s (%s)", slug, id)
	}
}

func reportUnitUpdated(slug, id string) {
	if !quiet {
		tprint("Successfully updated unit %s (%s)", slug, id)
	}
}

// awaitTriggersRemoval polls the unit until its awaiting/triggers apply gate
// clears, mirroring the cub CLI's --wait behavior for unit writes.
func awaitTriggersRemoval(unitDetails *goclient.Unit) error {
	var err error
	unitID := unitDetails.UnitID
	tries := 0
	numTries := 100
	ms := 25
	maxMs := 250
	done := false
	for tries < numTries {
		if unitDetails.ApplyGates == nil {
			done = true
			break
		}
		if _, awaitingTriggers := unitDetails.ApplyGates["awaiting/triggers"]; !awaitingTriggers {
			done = true
			break
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		ms *= 2
		if ms > maxMs {
			ms = maxMs
		}
		tries++
		unitDetails, err = cub.GetUnit(unitDetails.SpaceID, unitID)
		if err != nil {
			return err
		}
	}
	if !done {
		return errors.New("triggers didn't execute on unit " + unitDetails.Slug)
	}
	return nil
}

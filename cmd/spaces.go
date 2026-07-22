package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	goclient "github.com/confighub/sdk/core/openapi/goclient-new"
)

// AnnotationGeneratesSpaceID is the Space annotation stamped on a helm source
// space, recording the UUID of the base variant space its HelmSource units
// generate. It stands in for a future generator link type.
const AnnotationGeneratesSpaceID = "GeneratesSpaceID"

const (
	helmSourceSpaceSuffix = "-helm"
	baseSpaceSuffix       = "-base"
	variantLabelBase      = "base"
	variantLabelHelm      = "helm-source"

	toolchainKubernetesYAML = "Kubernetes/YAML"
	toolchainConfigHubYAML  = "ConfigHub/YAML"
)

// helmComponentSpaces holds the two spaces of a helm-installed component.
type helmComponentSpaces struct {
	source *goclient.Space
	base   *goclient.Space
}

// createHelmSpace creates a space with the given metadata.
func createHelmSpace(slug string, labels, annotations map[string]string) (*goclient.Space, error) {
	created, err := cub.CreateSpace(goclient.Space{
		Slug:        slug,
		Labels:      labels,
		Annotations: annotations,
	})
	if err != nil {
		return nil, err
	}
	tprint("Created space %s", slug)
	return created, nil
}

// ensureComponentSpaces gets or creates the component's base variant space and
// helm source space, stamping the component labels and the generator annotation.
func ensureComponentSpaces(component string) (*helmComponentSpaces, error) {
	baseSlug := component + baseSpaceSuffix
	sourceSlug := component + helmSourceSpaceSuffix

	base, err := cub.SpaceBySlug(baseSlug)
	if err != nil {
		return nil, err
	}
	if base == nil {
		base, err = createHelmSpace(baseSlug, map[string]string{
			"Component": component,
			"Variant":   variantLabelBase,
		}, nil)
		if err != nil {
			return nil, err
		}
	}

	source, err := cub.SpaceBySlug(sourceSlug)
	if err != nil {
		return nil, err
	}
	if source == nil {
		source, err = createHelmSpace(sourceSlug, map[string]string{
			"Component": component,
			"Variant":   variantLabelHelm,
		}, map[string]string{
			AnnotationGeneratesSpaceID: base.SpaceID.String(),
		})
		if err != nil {
			return nil, err
		}
	} else if source.Annotations[AnnotationGeneratesSpaceID] != base.SpaceID.String() {
		if err := patchHelmSpaceGeneratesAnnotation(source.SpaceID, base.SpaceID); err != nil {
			return nil, err
		}
	}

	return &helmComponentSpaces{source: source, base: base}, nil
}

// getComponentSpaces returns the component's spaces without creating anything,
// for commands that require a prior install.
func getComponentSpaces(component string) (*helmComponentSpaces, error) {
	base, err := cub.SpaceBySlug(component + baseSpaceSuffix)
	if err != nil {
		return nil, err
	}
	source, err := cub.SpaceBySlug(component + helmSourceSpaceSuffix)
	if err != nil {
		return nil, err
	}
	if source == nil || base == nil {
		return nil, fmt.Errorf("component %q has no helm source space; run 'cub helm install' first (or pass --component)", component)
	}
	return &helmComponentSpaces{source: source, base: base}, nil
}

// patchHelmSpaceGeneratesAnnotation sets the GeneratesSpaceID annotation on the
// helm source space.
func patchHelmSpaceGeneratesAnnotation(sourceSpaceID, baseSpaceID uuid.UUID) error {
	patchMap := map[string]any{
		"Annotations": map[string]any{
			AnnotationGeneratesSpaceID: baseSpaceID.String(),
		},
	}
	patchData, err := json.Marshal(patchMap)
	if err != nil {
		return err
	}
	if _, err := cub.PatchSpace(sourceSpaceID, patchData); err != nil {
		return fmt.Errorf("failed to set %s annotation on helm source space: %w", AnnotationGeneratesSpaceID, err)
	}
	return nil
}

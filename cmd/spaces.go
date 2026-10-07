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

	toolchainAppConfigYAML = "AppConfig/YAML"
)

// ensureSourceSpace gets or creates the component's helm source space, and
// points its generator annotation at the base space the upload wrote. The base
// itself is created by the upload.
func ensureSourceSpace(component string, baseSpaceID uuid.UUID) (*goclient.Space, error) {
	sourceSlug := component + helmSourceSpaceSuffix
	source, err := cub.SpaceBySlug(sourceSlug)
	if err != nil {
		return nil, err
	}
	if source == nil {
		source, err = cub.CreateSpace(goclient.Space{
			Slug: sourceSlug,
			Labels: map[string]string{
				"Component": component,
				"Variant":   variantLabelHelm,
			},
			Annotations: map[string]string{
				AnnotationGeneratesSpaceID: baseSpaceID.String(),
			},
		})
		if err != nil {
			return nil, err
		}
		tprint("Created space %s", sourceSlug)
		return source, nil
	}
	if source.Annotations[AnnotationGeneratesSpaceID] != baseSpaceID.String() {
		if err := patchHelmSpaceGeneratesAnnotation(source.SpaceID, baseSpaceID); err != nil {
			return nil, err
		}
	}
	return source, nil
}

// getSourceSpace returns the component's helm source space, requiring a prior
// install.
func getSourceSpace(component string) (*goclient.Space, error) {
	source, err := cub.SpaceBySlug(component + helmSourceSpaceSuffix)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("component %q has no helm source space; run 'cub helm install' first (or pass --component)", component)
	}
	return source, nil
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

package cmd

import "github.com/confighub/sdk/core/configkit/cubkit"

// makeSlug normalizes arbitrary text into a ConfigHub slug, matching the cub
// CLI's slug normalization.
func makeSlug(providedText string) string {
	return cubkit.CubNormalizeName(providedText)
}

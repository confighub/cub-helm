// Package cubclient is a thin wrapper around the ConfigHub SDK's goclient-new,
// scoped to the space and unit operations the helm plugin needs. It reads
// CUB_SERVER and CUB_TOKEN from the environment (set by cub when it invokes a
// plugin) and adds a Bearer auth header to every request.
package cubclient

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/confighub/sdk/core/cubapi"
	goclient "github.com/confighub/sdk/core/openapi/goclient-new"
	"github.com/google/uuid"
)

// Client talks to a ConfigHub server as the current cub session.
type Client struct {
	api    *goclient.ClientWithResponses
	cubapi *cubapi.Client
	ctx    context.Context
}

// New constructs a Client from CUB_SERVER and CUB_TOKEN. Both must be set;
// these are populated automatically when running as a cub plugin.
func New(ctx context.Context, userAgent string) (*Client, error) {
	if os.Getenv("CUB_SERVER") == "" {
		return nil, fmt.Errorf("CUB_SERVER not set; the helm plugin must be invoked as a cub plugin (try: cub helm ...)")
	}
	if os.Getenv("CUB_TOKEN") == "" {
		return nil, fmt.Errorf("CUB_TOKEN not set; run 'cub auth login' first")
	}
	c, err := cubapi.NewClientFromEnvironment(ctx, cubapi.ClientOptions{UserAgent: userAgent})
	if err != nil {
		return nil, fmt.Errorf("init client: %w", err)
	}
	return &Client{api: c.API, cubapi: c, ctx: ctx}, nil
}

// Upload sends rendered configuration to the server, which makes the
// component's base Space match it. With dryRun set nothing is written and the
// result describes what would happen.
func (c *Client) Upload(req goclient.UploadRequest, dryRun bool) (*goclient.UploadResult, error) {
	return cubapi.Upload(c.ctx, c.cubapi, req, dryRun)
}

// SpaceBySlug returns the space with the given slug, or nil if it does not exist.
func (c *Client) SpaceBySlug(slug string) (*goclient.Space, error) {
	where := "Slug = '" + slug + "'"
	res, err := c.api.ListSpacesWithResponse(c.ctx, &goclient.ListSpacesParams{Where: &where})
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	if res.JSON200 == nil {
		return nil, nil
	}
	for _, ext := range *res.JSON200 {
		if ext.Space != nil && ext.Space.Slug == slug {
			return ext.Space, nil
		}
	}
	return nil, nil
}

// CreateSpace creates a space and returns it.
func (c *Client) CreateSpace(space goclient.Space) (*goclient.Space, error) {
	res, err := c.api.CreateSpaceWithResponse(c.ctx, &goclient.CreateSpaceParams{}, space)
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("failed to create space %q: %s", space.Slug, res.Status())
	}
	return res.JSON200, nil
}

// PatchSpace applies a merge patch to a space.
func (c *Client) PatchSpace(spaceID uuid.UUID, patch []byte) (*goclient.Space, error) {
	res, err := c.api.PatchSpaceWithBodyWithResponse(c.ctx, spaceID, &goclient.PatchSpaceParams{},
		"application/merge-patch+json", bytes.NewReader(patch))
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	return res.JSON200, nil
}

// ListUnits returns the units in a space matching the optional where filter
// (pass "" for all). Configuration is not part of a unit; see GetUnitData.
func (c *Client) ListUnits(spaceID uuid.UUID, where string) ([]*goclient.Unit, error) {
	params := &goclient.ListUnitsParams{}
	if where != "" {
		params.Where = &where
	}
	res, err := c.api.ListUnitsWithResponse(c.ctx, spaceID, params)
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	if res.JSON200 == nil {
		return nil, nil
	}
	units := make([]*goclient.Unit, 0, len(*res.JSON200))
	for _, ext := range *res.JSON200 {
		if ext.Unit != nil {
			units = append(units, ext.Unit)
		}
	}
	return units, nil
}

// UnitBySlug returns the unit with the given slug in the space, or nil if it
// does not exist.
func (c *Client) UnitBySlug(spaceID uuid.UUID, slug string) (*goclient.Unit, error) {
	units, err := c.ListUnits(spaceID, "Slug = '"+slug+"'")
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		if u.Slug == slug {
			return u, nil
		}
	}
	return nil, nil
}

// CreateUnit creates a unit and returns it. Configuration is not a field of a
// unit; write it with PutUnitData once the unit exists.
func (c *Client) CreateUnit(spaceID uuid.UUID, unit goclient.Unit) (*goclient.Unit, error) {
	res, err := c.api.CreateUnitWithResponse(c.ctx, spaceID, &goclient.CreateUnitParams{}, unit)
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	return unitFromWrite(res.JSON200, res.Status())
}

// UpdateUnit updates a unit's metadata (labels, target, and so on) and returns
// the result. It does not touch the unit's configuration; see PutUnitData.
func (c *Client) UpdateUnit(spaceID uuid.UUID, unit *goclient.Unit) (*goclient.Unit, error) {
	res, err := c.api.UpdateUnitWithResponse(c.ctx, spaceID, unit.UnitID, &goclient.UpdateUnitParams{}, *unit)
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	return unitFromWrite(res.JSON200, res.Status())
}

// GetUnitData returns a unit's configuration as the server stores it (not
// base64-encoded).
func (c *Client) GetUnitData(spaceID, unitID uuid.UUID) (string, error) {
	res, err := c.api.DownloadUnitDataWithResponse(c.ctx, spaceID, unitID)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", fmt.Errorf("no response from server")
	}
	// The success body is the configuration itself, so there is no JSON200 for
	// cubapi.IsAPIError to inspect; check the status directly.
	if res.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("failed to fetch data of unit %s: %s", unitID, res.Status())
	}
	return string(res.Body), nil
}

// PutUnitData replaces a unit's configuration and returns the unit as it
// stands after the write, including any apply gates the write set.
func (c *Client) PutUnitData(spaceID, unitID uuid.UUID, data string) (*goclient.Unit, error) {
	res, err := c.api.UploadUnitDataWithBodyWithResponse(c.ctx, spaceID, unitID,
		&goclient.UploadUnitDataParams{}, "application/octet-stream", strings.NewReader(data))
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	return unitFromWrite(res.JSON200, res.Status())
}

// unitFromWrite extracts the unit from a create, update, or data-write
// response. A write answers with the operation's result rather than the
// entity; the unit is inside it.
func unitFromWrite(resp *goclient.UnitCreateOrUpdateResponse, status string) (*goclient.Unit, error) {
	if resp == nil || resp.Unit == nil {
		return nil, fmt.Errorf("the server returned no unit (status %s)", status)
	}
	return resp.Unit, nil
}

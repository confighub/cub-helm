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
	ctx    context.Context
	server string
}

// New constructs a Client from CUB_SERVER and CUB_TOKEN. Both must be set;
// these are populated automatically when running as a cub plugin.
func New(ctx context.Context) (*Client, error) {
	server := os.Getenv("CUB_SERVER")
	if server == "" {
		return nil, fmt.Errorf("CUB_SERVER not set; the helm plugin must be invoked as a cub plugin (try: cub helm ...)")
	}
	token := os.Getenv("CUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("CUB_TOKEN not set; run 'cub auth login' first")
	}

	authHeader := "Bearer " + token
	api, err := goclient.NewClientWithResponses(strings.TrimRight(server, "/")+"/api",
		goclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", authHeader)
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("init client: %w", err)
	}
	return &Client{api: api, ctx: ctx, server: server}, nil
}

// Context returns the context the client was constructed with.
func (c *Client) Context() context.Context { return c.ctx }

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
// (pass "" for all). Configuration is not part of a unit; see ListUnitData.
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

// GetUnit fetches a single unit with all fields (including ApplyGates).
func (c *Client) GetUnit(spaceID, unitID uuid.UUID) (*goclient.Unit, error) {
	res, err := c.api.GetUnitWithResponse(c.ctx, spaceID, unitID, &goclient.GetUnitParams{})
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	if res.JSON200 == nil || res.JSON200.Unit == nil {
		return nil, fmt.Errorf("unit %s not found", unitID)
	}
	return res.JSON200.Unit, nil
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

// DeleteUnit deletes a unit.
func (c *Client) DeleteUnit(spaceID, unitID uuid.UUID) error {
	res, err := c.api.DeleteUnitWithResponse(c.ctx, spaceID, unitID)
	if cubapi.IsAPIError(err, res) {
		return cubapi.InterpretErrorGeneric(err, res)
	}
	return nil
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

// ListUnitData returns the configuration of every unit in the space matching
// the optional where filter (pass "" for all), keyed by unit ID, in a single
// request. Use it alongside ListUnits when the units' content is needed too.
func (c *Client) ListUnitData(spaceID uuid.UUID, where string) (map[uuid.UUID]string, error) {
	// The data search endpoint is organization-wide, so the space is part of
	// the where clause. The filter grammar has no grouping; AND is the only
	// conjunction, so plain concatenation is unambiguous.
	scoped := fmt.Sprintf("SpaceID = '%s'", spaceID)
	if where != "" {
		scoped = where + " AND " + scoped
	}
	res, err := c.api.SearchUnitDataWithResponse(c.ctx, &goclient.SearchUnitDataParams{Where: &scoped})
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	data := map[uuid.UUID]string{}
	if res.JSON200 == nil {
		return data, nil
	}
	for _, row := range *res.JSON200 {
		data[row.UnitID] = row.Data
	}
	return data, nil
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

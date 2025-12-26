// Package cloudflare is a minimal client for the parts of the Cloudflare API
// this tool needs: finding a zone, finding a DNS record, and updating or
// creating it.
//
// https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/update/
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BaseURL is the API root; a variable so tests can point it at a stub server.
var BaseURL = "https://api.cloudflare.com/client/v4"

// Client talks to the Cloudflare API with a scoped API token.
type Client struct {
	token  string
	http   *http.Client
	zoneID map[string]string // zone name -> zone id, cached for the process
}

// New returns a client authenticated with the given API token. The token needs
// Zone:Read and DNS:Edit on the zones being updated.
func New(token string) *Client {
	return &Client{
		token:  token,
		http:   &http.Client{Timeout: 30 * time.Second},
		zoneID: map[string]string{},
	}
}

// Record is a DNS record, used both for reads and for update/create bodies.
type Record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	// Comment is read-only for our purposes: it is omitted from update bodies
	// so a PATCH never clobbers a comment set in the dashboard.
	Comment string `json:"comment,omitempty"`
}

// APIError is a non-success response from the API.
type APIError struct {
	Status   int
	Messages []string
}

func (e *APIError) Error() string {
	if len(e.Messages) == 0 {
		return fmt.Sprintf("cloudflare: http %d", e.Status)
	}
	return "cloudflare: " + strings.Join(e.Messages, "; ")
}

// Retryable reports whether the request is worth trying again later: transient
// server trouble or rate limiting, as opposed to a bad token or a typo.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
	// ResultInfo is present on list endpoints and drives pagination.
	ResultInfo struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		Count      int `json:"count"`
		TotalCount int `json:"total_count"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	_, err := c.doPaged(ctx, method, path, body, out)
	return err
}

// doPaged is do, plus the result_info a list endpoint returns.
func (c *Client) doPaged(ctx context.Context, method, path string, body, out any) (pageInfo, error) {
	var empty pageInfo
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return empty, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, rdr)
	if err != nil {
		return empty, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return empty, err
	}
	defer func() { _ = resp.Body.Close() }()

	var env envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&env); err != nil {
		return empty, fmt.Errorf("cloudflare: http %d: unreadable response: %w", resp.StatusCode, err)
	}
	if !env.Success {
		apiErr := &APIError{Status: resp.StatusCode}
		for _, e := range env.Errors {
			apiErr.Messages = append(apiErr.Messages, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		return empty, apiErr
	}
	info := pageInfo{page: env.ResultInfo.Page, totalPages: env.ResultInfo.TotalPages, count: env.ResultInfo.Count}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return info, err
		}
	}
	return info, nil
}

// pageInfo is the subset of result_info needed to walk a list endpoint.
type pageInfo struct {
	page       int
	totalPages int
	count      int
}

// more reports whether another page follows the one just read. total_pages is
// not always populated, so a full page is also treated as "keep going".
func (p pageInfo) more(perPage int) bool {
	if p.totalPages > 0 {
		return p.page < p.totalPages
	}
	return p.count == perPage
}

// ZoneID resolves a zone name to its id, caching the result.
func (c *Client) ZoneID(ctx context.Context, zone string) (string, error) {
	if id, ok := c.zoneID[zone]; ok {
		return id, nil
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	q := "/zones?name=" + url.QueryEscape(zone)
	if err := c.do(ctx, http.MethodGet, q, nil, &zones); err != nil {
		return "", err
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("zone %q not found; check the name and the token's zone permissions", zone)
	}
	c.zoneID[zone] = zones[0].ID
	return zones[0].ID, nil
}

// FindRecord returns the record with the given name and type, or nil when the
// zone has no such record.
func (c *Client) FindRecord(ctx context.Context, zoneID, name, typ string) (*Record, error) {
	q := fmt.Sprintf("/zones/%s/dns_records?type=%s&name=%s",
		zoneID, url.QueryEscape(typ), url.QueryEscape(name))
	var recs []Record
	if err := c.do(ctx, http.MethodGet, q, nil, &recs); err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return &recs[0], nil
}

// UpdateRecord overwrites an existing record (PATCH /zones/{id}/dns_records/{id}).
func (c *Client) UpdateRecord(ctx context.Context, zoneID, recordID string, r Record) error {
	r.ID = ""
	path := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID)
	return c.do(ctx, http.MethodPatch, path, r, nil)
}

// CreateRecord adds a new record to the zone.
func (c *Client) CreateRecord(ctx context.Context, zoneID string, r Record) error {
	r.ID = ""
	return c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", r, nil)
}

// perPage is the largest page size the API accepts for these list endpoints.
const perPage = 100

// Zone is a zone the API token can see.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ListZones returns every zone the token has access to, following pagination.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	for page := 1; ; page++ {
		var batch []Zone
		q := fmt.Sprintf("/zones?page=%d&per_page=%d", page, perPage)
		info, err := c.doPaged(ctx, http.MethodGet, q, nil, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if !info.more(perPage) {
			break
		}
	}
	// Cache what we learned so a later ZoneID lookup costs nothing.
	for _, z := range all {
		c.zoneID[z.Name] = z.ID
	}
	return all, nil
}

// ListRecords returns the zone's DNS records of the given types, following
// pagination. Pass no types to list every record in the zone.
//
// https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/list/
func (c *Client) ListRecords(ctx context.Context, zoneID string, types ...string) ([]Record, error) {
	if len(types) == 0 {
		types = []string{""}
	}
	var all []Record
	for _, typ := range types {
		for page := 1; ; page++ {
			var batch []Record
			q := fmt.Sprintf("/zones/%s/dns_records?page=%d&per_page=%d&order=name&direction=asc",
				zoneID, page, perPage)
			if typ != "" {
				q += "&type=" + url.QueryEscape(typ)
			}
			info, err := c.doPaged(ctx, http.MethodGet, q, nil, &batch)
			if err != nil {
				return nil, err
			}
			all = append(all, batch...)
			if !info.more(perPage) {
				break
			}
		}
	}
	return all, nil
}

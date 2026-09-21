package controlplane

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ListAPIKeys follows bounded server pages without returning partial results on
// an authentication or transport failure. Secrets are never part of this API.
func (c *CLIClient) ListAPIKeys(ctx context.Context) (APIKeyList, error) {
	result := APIKeyList{Keys: []APIKey{}}
	cursor := ""
	for {
		var page APIKeyList
		path := "/v1/api-keys?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		if err := c.request(ctx, http.MethodGet, path, "", nil, &page); err != nil {
			return APIKeyList{}, err
		}
		result.Enabled = page.Enabled
		result.Keys = append(result.Keys, page.Keys...)
		if page.NextCursor == "" {
			return result, nil
		}
		if page.NextCursor <= cursor {
			return APIKeyList{}, errors.New("invalid API-key pagination response")
		}
		cursor = page.NextCursor
	}
}

// CreateAPIKey returns the secret once. An empty expiresAt explicitly requests
// no expiration; callers choose their default before issuing the request.
func (c *CLIClient) CreateAPIKey(ctx context.Context, name, expiresAt string) (APIKeyCreated, error) {
	var result APIKeyCreated
	body, err := json.Marshal(APIKeyCreateInput{Name: name, ExpiresAt: &expiresAt})
	if err != nil {
		return result, err
	}
	err = c.request(ctx, http.MethodPost, "/v1/api-keys", "application/json", bytes.NewReader(body), &result)
	return result, err
}

func (c *CLIClient) RevokeAPIKey(ctx context.Context, id string) (APIKey, error) {
	// Reject path syntax before constructing a request. Identifiers are public,
	// fixed-format values; never accept a secret token in this position.
	if len(id) != 36 || !strings.HasPrefix(id, "key_") {
		return APIKey{}, errors.New("invalid API-key ID; use the ID from auth keys list")
	}
	if _, err := hex.DecodeString(id[4:]); err != nil {
		return APIKey{}, errors.New("invalid API-key ID; use the ID from auth keys list")
	}
	var result struct {
		Key APIKey `json:"key"`
	}
	err := c.request(ctx, http.MethodDelete, "/v1/api-keys/"+id, "", nil, &result)
	return result.Key, err
}

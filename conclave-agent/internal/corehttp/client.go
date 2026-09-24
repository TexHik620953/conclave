// Package corehttp is a small HTTP client for the conclave-core REST API.
package corehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to conclave-core over HTTP.
type Client struct {
	base        string
	userToken   string
	deviceToken string
	http        *http.Client
}

// New creates a client.
func New(base, userToken, deviceToken string) *Client {
	return &Client{
		base:        base,
		userToken:   userToken,
		deviceToken: deviceToken,
		http:        &http.Client{Timeout: 5 * time.Minute},
	}
}

// Auth selects which token to send.
type Auth int

const (
	// AuthUser uses the user token (client API).
	AuthUser Auth = iota
	// AuthDevice uses the device token (agent endpoints).
	AuthDevice
)

// DeviceInfo describes the authenticated device.
type DeviceInfo struct {
	DeviceID string `json:"device_id"`
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
}

// DeviceMe fetches the device for the configured device token.
func (c *Client) DeviceMe(ctx context.Context) (DeviceInfo, error) {
	body, status, err := c.Do(ctx, http.MethodGet, "/v1/devices/me", nil, AuthDevice)
	if err != nil {
		return DeviceInfo{}, err
	}
	if status != http.StatusOK {
		return DeviceInfo{}, fmt.Errorf("GET /v1/devices/me: HTTP %d: %s", status, string(body))
	}
	var info DeviceInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return DeviceInfo{}, err
	}
	return info, nil
}

// Do performs a request with the given auth token.
func (c *Client) Do(ctx context.Context, method, path string, body any, auth Auth) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token := c.userToken
	if auth == AuthDevice {
		token = c.deviceToken
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return data, resp.StatusCode, nil
}

// Base returns the core base URL.
func (c *Client) Base() string { return c.base }

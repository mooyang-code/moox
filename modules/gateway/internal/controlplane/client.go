// Package controlplane synchronizes node-local routes with Admin.
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/gateway/internal/config"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

const DefaultControlKeyID = "moox-gateway-control"
const maxSnapshotBytes = 16 << 20

type Options struct {
	NodeID, BaseURL, HMACKeyFile, CAFile, KeyID string
	Now                                         func() time.Time
}

type Client struct {
	nodeID, baseURL string
	credentials     gatewayauth.Credentials
	httpClient      *http.Client
	now             func() time.Time
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.NodeID) == "" {
		return nil, errors.New("control-plane node ID is required")
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if err := config.ValidateBaseURL(baseURL); err != nil {
		return nil, errors.New("control-plane base URL is invalid")
	}
	secret, err := config.ReadSecret(options.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read control-plane key: %w", err)
	}
	keyID := strings.TrimSpace(options.KeyID)
	if keyID == "" {
		keyID = DefaultControlKeyID
	}
	httpClient, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: 10 * time.Second, CAFile: options.CAFile})
	if err != nil {
		return nil, err
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{nodeID: options.NodeID, baseURL: baseURL, credentials: gatewayauth.Credentials{KeyID: keyID, Secret: secret}, httpClient: httpClient, now: now}, nil
}

func (client *Client) Pull(ctx context.Context, currentHash string) (gatewayproxy.Snapshot, error) {
	endpoint, err := url.Parse(client.baseURL + "/api/gateway-control/routes")
	if err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	query := endpoint.Query()
	query.Set("node_id", client.nodeID)
	if currentHash != "" {
		query.Set("current_hash", currentHash)
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	if err := client.sign(request, nil); err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return gatewayproxy.Snapshot{}, fmt.Errorf("pull gateway routes: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return gatewayproxy.Snapshot{}, fmt.Errorf("pull gateway routes: unexpected HTTP status %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxSnapshotBytes+1))
	if err != nil {
		return gatewayproxy.Snapshot{}, fmt.Errorf("read gateway routes: %w", err)
	}
	if len(encoded) > maxSnapshotBytes {
		return gatewayproxy.Snapshot{}, errors.New("gateway route snapshot is too large")
	}
	var snapshot gatewayproxy.Snapshot
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return gatewayproxy.Snapshot{}, fmt.Errorf("decode gateway routes: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return gatewayproxy.Snapshot{}, errors.New("gateway route snapshot contains trailing JSON")
	}
	if snapshot.NodeID != client.nodeID {
		return gatewayproxy.Snapshot{}, fmt.Errorf("gateway route snapshot targets %q, want %q", snapshot.NodeID, client.nodeID)
	}
	var table gatewayproxy.Table
	if err := table.Replace(snapshot); err != nil {
		return gatewayproxy.Snapshot{}, fmt.Errorf("validate gateway routes: %w", err)
	}
	return snapshot, nil
}

func (client *Client) Report(ctx context.Context, appliedHash string, routeCount int32, lastError string) error {
	body, err := json.Marshal(struct {
		NodeID           string `json:"node_id"`
		AppliedRouteHash string `json:"applied_route_hash"`
		RouteCount       int32  `json:"route_count"`
		LastError        string `json:"last_error"`
	}{client.nodeID, appliedHash, routeCount, lastError})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/api/gateway-control/status", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if err := client.sign(request, body); err != nil {
		return err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("report gateway status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("report gateway status: unexpected HTTP status %d", response.StatusCode)
	}
	return nil
}

func (client *Client) sign(request *http.Request, body []byte) error {
	headers, err := gatewayauth.Sign(client.credentials, gatewayauth.Request{Method: request.Method, Path: request.URL.EscapedPath(), TargetNode: client.nodeID, Body: body}, client.now())
	if err != nil {
		return err
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	return nil
}

package gateway

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

const (
	gatewayControlDefaultKeyID = "moox-gateway-control"
	gatewayControlMaxBodyBytes = 64 << 10
	gatewayControlMaxRoutes    = 10000
)

type GatewayControlProvider interface {
	CompileGatewaySnapshot(context.Context, string) (gatewayproxy.Snapshot, error)
	ReportGatewayStatus(context.Context, gatewayproxy.GatewayStatusReport) error
}

type GatewayProvider interface {
	GatewayControlProvider
	AdminServiceDetailProvider
}

type gatewayStatusRequest struct {
	NodeID           string `json:"node_id"`
	AppliedRouteHash string `json:"applied_route_hash"`
	RouteCount       int32  `json:"route_count"`
	LastError        string `json:"last_error"`
}

func (hr *HTTPRouter) handleGatewayRoutes(w http.ResponseWriter, r *http.Request) {
	if hasRequestBody(r) {
		writeGatewayControlError(w, http.StatusBadRequest, "invalid request")
		return
	}
	values, ok := r.URL.Query()["node_id"]
	if !ok || len(values) != 1 {
		writeGatewayControlAuthError(w)
		return
	}
	nodeID := values[0]
	if !hr.authenticateGatewayControl(w, r, nodeID, nil) {
		return
	}
	snapshot, err := hr.controlProvider.CompileGatewaySnapshot(r.Context(), nodeID)
	if err != nil {
		writeGatewayControlProviderError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (hr *HTTPRouter) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	body, status, err := readGatewayControlBody(r)
	if err != nil {
		writeGatewayControlError(w, status, "invalid request")
		return
	}
	var request gatewayStatusRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeGatewayControlError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := requireJSONEOF(decoder); err != nil || validateGatewayStatusRequest(request) != nil {
		writeGatewayControlError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !hr.authenticateGatewayControl(w, r, request.NodeID, body) {
		return
	}
	report := gatewayproxy.GatewayStatusReport{
		NodeID: request.NodeID, AppliedRouteHash: request.AppliedRouteHash,
		RouteCount: request.RouteCount, LastSeenAt: time.Now().UTC(), LastError: request.LastError,
	}
	if err := hr.controlProvider.ReportGatewayStatus(r.Context(), report); err != nil {
		writeGatewayControlProviderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (hr *HTTPRouter) authenticateGatewayControl(w http.ResponseWriter, r *http.Request, targetNode string, body []byte) bool {
	secret := os.Getenv("MOOX_GATEWAY_CONTROL_SECRET_KEY")
	store := getRequestAuthStore()
	if strings.TrimSpace(secret) == "" || store == nil {
		writeGatewayControlError(w, http.StatusServiceUnavailable, "gateway control unavailable")
		return false
	}
	keyID := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CONTROL_KEY_ID"))
	if keyID == "" {
		keyID = gatewayControlDefaultKeyID
	}
	claims, err := gatewayauth.Verify(
		gatewayauth.Credentials{KeyID: keyID, Secret: secret},
		gatewayauth.Request{Method: r.Method, Path: r.URL.EscapedPath(), TargetNode: targetNode, Body: body},
		r.Header, time.Now(),
	)
	if err != nil {
		writeGatewayControlAuthError(w)
		return false
	}
	consumed, err := store.ConsumeGatewayControlNonce(r.Context(), claims.KeyID, claims.Nonce, claims.TTL)
	if err != nil {
		writeGatewayControlError(w, http.StatusServiceUnavailable, "gateway control unavailable")
		return false
	}
	if !consumed {
		writeGatewayControlAuthError(w)
		return false
	}
	return true
}

func readGatewayControlBody(r *http.Request) ([]byte, int, error) {
	if r.Body == nil {
		return nil, http.StatusBadRequest, errors.New("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, gatewayControlMaxBodyBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if len(body) > gatewayControlMaxBodyBytes {
		return nil, http.StatusRequestEntityTooLarge, errors.New("request body too large")
	}
	return body, 0, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateGatewayStatusRequest(request gatewayStatusRequest) error {
	if strings.TrimSpace(request.NodeID) == "" || request.NodeID != strings.TrimSpace(request.NodeID) {
		return errors.New("node_id is required")
	}
	if request.AppliedRouteHash != "" {
		decoded, err := hex.DecodeString(request.AppliedRouteHash)
		if err != nil || len(decoded) != 32 || request.AppliedRouteHash != strings.ToLower(request.AppliedRouteHash) {
			return errors.New("applied_route_hash must be lowercase SHA-256")
		}
	}
	if request.RouteCount < 0 || request.RouteCount > gatewayControlMaxRoutes {
		return errors.New("route_count is out of range")
	}
	if len(request.LastError) > 1024 {
		return errors.New("last_error is too long")
	}
	return nil
}

func hasRequestBody(r *http.Request) bool {
	return r.Body != nil && (r.ContentLength != 0 || len(r.TransferEncoding) > 0)
}

func writeGatewayControlProviderError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, gatewayproxy.ErrGatewayNodeNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, gatewayproxy.ErrInvalidGatewayRoute) {
		status = http.StatusBadRequest
	}
	writeGatewayControlError(w, status, http.StatusText(status))
}

func writeGatewayControlAuthError(w http.ResponseWriter) {
	writeGatewayControlError(w, http.StatusUnauthorized, "authentication failed")
}

func writeGatewayControlError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

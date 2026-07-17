package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	adminsecurity "github.com/mooyang-code/moox/modules/admin/internal/security"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/requestauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

type requestAuthStore interface {
	GetSigningSession(context.Context, string) (*authmodel.RequestSigningSession, error)
	ConsumeSessionNonce(context.Context, string, string, time.Duration) (bool, error)
	ConsumeGatewayControlNonce(context.Context, string, string, time.Duration) (bool, error)
	ConsumeRawSessionTicket(context.Context, string) (*authmodel.RawSessionTicket, error)
}

var requestAuthState struct {
	sync.RWMutex
	store requestAuthStore
}

var rawSessionOwnerState struct {
	sync.RWMutex
	verify func(sessionID, userID string) bool
}

func SetRawSessionOwnerVerifier(verify func(sessionID, userID string) bool) {
	rawSessionOwnerState.Lock()
	rawSessionOwnerState.verify = verify
	rawSessionOwnerState.Unlock()
}

func rawSessionBelongsToUser(sessionID, userID string) bool {
	rawSessionOwnerState.RLock()
	verify := rawSessionOwnerState.verify
	rawSessionOwnerState.RUnlock()
	return verify != nil && verify(sessionID, userID)
}

func SetRequestAuthStore(store requestAuthStore) {
	requestAuthState.Lock()
	requestAuthState.store = store
	requestAuthState.Unlock()
}

func getRequestAuthStore() requestAuthStore {
	requestAuthState.RLock()
	defer requestAuthState.RUnlock()
	return requestAuthState.store
}

func verifyAdminRequest(r *http.Request, body []byte) (*accessClaims, error) {
	claims, ok := validateAccessToken(r.Context(), accessTokenFromHTTP(r))
	if !ok {
		return nil, errors.New("invalid access token")
	}
	store := getRequestAuthStore()
	if store == nil {
		return nil, errors.New("request auth store is unavailable")
	}
	session, err := store.GetSigningSession(r.Context(), claims.SessionID)
	if err != nil || session.UserID != claims.UserID || !time.Now().Before(session.ExpiresAt) {
		return nil, errors.New("invalid signing session")
	}
	timestamp, err := strconv.ParseInt(r.Header.Get("X-Moox-Timestamp"), 10, 64)
	if err != nil {
		return nil, errors.New("invalid request timestamp")
	}
	skew := time.Minute
	nonceTTL := 2 * time.Minute
	if cfg := GetConfig(); cfg != nil {
		if cfg.Security.RequestClockSkew > 0 {
			skew = cfg.Security.RequestClockSkew
		}
		if cfg.Security.NonceTTL > 0 {
			nonceTTL = cfg.Security.NonceTTL
		}
	}
	requestTime := time.Unix(timestamp, 0)
	if delta := time.Since(requestTime); delta > skew || delta < -skew {
		return nil, errors.New("request timestamp outside allowed window")
	}
	nonce := r.Header.Get("X-Moox-Nonce")
	material := requestauth.Material{Method: r.Method, Path: r.URL.EscapedPath(), Body: body, Headers: signedGatewayHeaders(r), Timestamp: timestamp, Nonce: nonce}
	encryptionKey, err := adminsecurity.GetEncryptionKey()
	if err != nil {
		return nil, errors.New("encryption key unavailable")
	}
	secret, err := mooxsecurity.Decrypt(session.EncryptedSecret, encryptionKey)
	if err != nil {
		return nil, errors.New("invalid signing session secret")
	}
	if err := requestauth.Verify(secret, material, r.Header.Get("X-Moox-Signature")); err != nil {
		return nil, fmt.Errorf("invalid request signature: %w", err)
	}
	consumed, err := store.ConsumeSessionNonce(r.Context(), claims.SessionID, nonce, nonceTTL)
	if err != nil || !consumed {
		return nil, errors.New("request nonce was already used")
	}
	return claims, nil
}

func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := readBoundedBody(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func accessTokenFromHTTP(r *http.Request) string {
	token := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Access-Token"))
	}
	return token
}

func sessionIDForLog(r *http.Request) string {
	claims, ok := validateAccessToken(r.Context(), accessTokenFromHTTP(r))
	if !ok {
		return ""
	}
	return claims.SessionID
}

func shouldSkipAdminRequestAuth(path string) bool { return ShouldSkipAuth(path) }

func writeAdminAuthFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(&middlewareResp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "访问令牌无效，请退出重新登录(gateway)"}})
}

var rawRouteOperations = map[string]string{
	"ssh/WsConnect": "ssh_ws", "ssh/SftpDownload": "sftp_download", "ssh/SftpUpload": "sftp_upload",
}

func isRawTicketPath(path string) bool {
	_, ok := rawRouteOperations[strings.TrimPrefix(path, "/api/admin/")]
	return ok
}

func validateRawRouteTicket(r *http.Request, serviceID, method string) (*accessClaims, error) {
	operation, ok := rawRouteOperations[serviceID+"/"+method]
	if !ok {
		return nil, errors.New("raw route does not accept tickets")
	}
	store := getRequestAuthStore()
	if store == nil {
		return nil, errors.New("request auth store is unavailable")
	}
	ticket, err := store.ConsumeRawSessionTicket(r.Context(), r.URL.Query().Get("ticket"))
	if err != nil || ticket.Operation != operation || !time.Now().Before(ticket.ExpiresAt) {
		return nil, errors.New("invalid raw route ticket")
	}
	requestSessionID := r.URL.Query().Get("session_id")
	if requestSessionID == "" || requestSessionID != ticket.ResourceSessionID || !rawSessionBelongsToUser(requestSessionID, ticket.UserID) {
		return nil, errors.New("raw route session does not match ticket owner")
	}
	session, err := store.GetSigningSession(r.Context(), ticket.SessionID)
	if err != nil || session.UserID != ticket.UserID || !time.Now().Before(session.ExpiresAt) {
		return nil, errors.New("invalid raw route session")
	}
	return &accessClaims{UserID: ticket.UserID, SessionID: ticket.SessionID, ExpiresAt: session.ExpiresAt}, nil
}

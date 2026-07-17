package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/packages/requestauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRequestAuthStore struct {
	sessions map[string]authmodel.RequestSigningSession
	nonces   map[string]bool
	tickets  map[string]authmodel.RawSessionTicket
}

func (s *fakeRequestAuthStore) GetSigningSession(_ context.Context, sid string) (*authmodel.RequestSigningSession, error) {
	v, ok := s.sessions[sid]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return &v, nil
}
func (s *fakeRequestAuthStore) ConsumeSessionNonce(_ context.Context, sid, nonce string, _ time.Duration) (bool, error) {
	k := sid + ":" + nonce
	if s.nonces[k] {
		return false, nil
	}
	s.nonces[k] = true
	return true, nil
}

func (s *fakeRequestAuthStore) ConsumeGatewayControlNonce(_ context.Context, keyID, nonce string, _ time.Duration) (bool, error) {
	key := "gateway_control:" + keyID + ":" + nonce
	if s.nonces[key] {
		return false, nil
	}
	s.nonces[key] = true
	return true, nil
}
func (s *fakeRequestAuthStore) ConsumeRawSessionTicket(_ context.Context, id string) (*authmodel.RawSessionTicket, error) {
	v, ok := s.tickets[id]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	delete(s.tickets, id)
	return &v, nil
}

func signedAdminRequest(t *testing.T, secret, key, sid, path string, body []byte, timestamp int64, nonce string) *http.Request {
	t.Helper()
	token, err := mooxsecurity.SignToken(map[string]any{"user_id": "u1", "username": "admin", "role": 2, "token_type": "access", "sid": sid}, secret, "moox-admin", time.Hour)
	require.NoError(t, err)
	sig, err := requestauth.Sign(key, requestauth.Material{Method: http.MethodPost, Path: path, Body: body, Timestamp: timestamp, Nonce: nonce})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set("Authorization", token)
	r.Header.Set("X-Moox-Timestamp", fmt.Sprint(timestamp))
	r.Header.Set("X-Moox-Nonce", nonce)
	r.Header.Set("X-Moox-Signature", sig)
	return r
}

func setupRequestAuthTest(t *testing.T) (*fakeRequestAuthStore, string, string, string) {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "encryption-key-for-tests")
	secret, key, sid := "jwt-secret-for-request-auth", "signing-key", "sid-1"
	encrypted, err := mooxsecurity.Encrypt(key, "encryption-key-for-tests")
	require.NoError(t, err)
	store := &fakeRequestAuthStore{sessions: map[string]authmodel.RequestSigningSession{sid: {SessionID: sid, UserID: "u1", EncryptedSecret: encrypted, ExpiresAt: time.Now().Add(time.Hour)}}, nonces: map[string]bool{}, tickets: map[string]authmodel.RawSessionTicket{}}
	SetRequestAuthStore(store)
	SetConfig(&Config{JWT: JWTConfig{SecretKey: secret}, Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/auth/GetLoginSalt", "/api/admin/auth/Login"}}})
	return store, secret, key, sid
}

func TestAdminRequestRequiresJWTAndSignature(t *testing.T) {
	setupRequestAuthTest(t)
	_, err := verifyAdminRequest(httptest.NewRequest(http.MethodPost, "/api/admin/auth/GetUserInfo", nil), nil)
	require.Error(t, err)
}

func TestAdminRequestRejectsExpiredSession(t *testing.T) {
	store, secret, key, sid := setupRequestAuthTest(t)
	v := store.sessions[sid]
	v.ExpiresAt = time.Now().Add(-time.Second)
	store.sessions[sid] = v
	r := signedAdminRequest(t, secret, key, sid, "/api/admin/auth/GetUserInfo", nil, time.Now().Unix(), "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	_, err := verifyAdminRequest(r, nil)
	require.Error(t, err)
}

func TestAdminRequestRejectsTimestampOutsideWindow(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	nonce := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := signedAdminRequest(t, secret, key, sid, "/api/admin/auth/GetUserInfo", nil, time.Now().Add(-2*time.Minute).Unix(), nonce)
	_, err := verifyAdminRequest(r, nil)
	require.Error(t, err)
}

func TestAdminRequestRejectsChangedBodyOrPath(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	nonce := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	r := signedAdminRequest(t, secret, key, sid, "/api/admin/auth/GetUserInfo", []byte(`{"a":1}`), time.Now().Unix(), nonce)
	_, err := verifyAdminRequest(r, []byte(`{"a":2}`))
	require.Error(t, err)
}

func TestAdminRequestRejectsChangedSpaceHeader(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	timestamp := time.Now().Unix()
	nonce := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	path := "/api/admin/auth/GetUserInfo"
	token, err := mooxsecurity.SignToken(map[string]any{"user_id": "u1", "username": "admin", "role": 2, "token_type": "access", "sid": sid}, secret, "moox-admin", time.Hour)
	require.NoError(t, err)
	sig, err := requestauth.Sign(key, requestauth.Material{
		Method: http.MethodPost, Path: path, Headers: map[string]string{"X-Space-Id": "space-1"}, Timestamp: timestamp, Nonce: nonce,
	})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set("Authorization", token)
	r.Header.Set("X-Moox-Timestamp", fmt.Sprint(timestamp))
	r.Header.Set("X-Moox-Nonce", nonce)
	r.Header.Set("X-Moox-Signature", sig)
	r.Header.Set("X-Space-Id", "space-2")

	_, err = verifyAdminRequest(r, nil)
	require.Error(t, err)
}

func TestAdminRequestRejectsReplayedNonce(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	nonce := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	r := signedAdminRequest(t, secret, key, sid, "/api/admin/auth/GetUserInfo", nil, time.Now().Unix(), nonce)
	_, err := verifyAdminRequest(r, nil)
	require.NoError(t, err)
	_, err = verifyAdminRequest(r, nil)
	require.Error(t, err)
}

func TestLoginRoutesRemainUnsigned(t *testing.T) {
	setupRequestAuthTest(t)
	assert.True(t, shouldSkipAdminRequestAuth("/api/admin/auth/Login"))
	assert.True(t, shouldSkipAdminRequestAuth("/api/admin/auth/GetLoginSalt"))
}

func TestRawTicketRequiresMatchingOwnedSSHSession(t *testing.T) {
	store, _, _, sid := setupRequestAuthTest(t)
	SetRawSessionOwnerVerifier(func(sessionID, userID string) bool {
		return sessionID == "ssh-owned" && userID == "u1"
	})
	t.Cleanup(func() { SetRawSessionOwnerVerifier(nil) })
	store.tickets["matching"] = authmodel.RawSessionTicket{TicketID: "matching", SessionID: sid, UserID: "u1", Operation: "ssh_ws", ResourceSessionID: "ssh-owned", ExpiresAt: time.Now().Add(time.Minute)}

	matching := httptest.NewRequest(http.MethodGet, "/api/admin/ssh/WsConnect?ticket=matching&session_id=ssh-owned", nil)
	_, err := validateRawRouteTicket(matching, "ssh", "WsConnect")
	require.NoError(t, err)

	store.tickets["mismatch"] = authmodel.RawSessionTicket{TicketID: "mismatch", SessionID: sid, UserID: "u1", Operation: "ssh_ws", ResourceSessionID: "ssh-owned", ExpiresAt: time.Now().Add(time.Minute)}
	mismatch := httptest.NewRequest(http.MethodGet, "/api/admin/ssh/WsConnect?ticket=mismatch&session_id=ssh-other", nil)
	_, err = validateRawRouteTicket(mismatch, "ssh", "WsConnect")
	require.Error(t, err)

	store.tickets["unowned"] = authmodel.RawSessionTicket{TicketID: "unowned", SessionID: sid, UserID: "u1", Operation: "ssh_ws", ResourceSessionID: "ssh-unowned", ExpiresAt: time.Now().Add(time.Minute)}
	unowned := httptest.NewRequest(http.MethodGet, "/api/admin/ssh/WsConnect?ticket=unowned&session_id=ssh-unowned", nil)
	_, err = validateRawRouteTicket(unowned, "ssh", "WsConnect")
	require.Error(t, err)
}

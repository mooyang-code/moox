//go:build e2e_external

package router

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/stretchr/testify/require"
)

func TestExternalGatewayOwnerHandler(t *testing.T) {
	coord := os.Getenv("MOOX_GATEWAY_OWNER_E2E_COORD")
	require.NotEmpty(t, coord)
	address, err := os.ReadFile(filepath.Join(coord, "trade-ready"))
	require.NoError(t, err)
	snapshot, err := gatewayproxy.NormalizeAndHash("gateway-owner-e2e", []gatewayproxy.Route{{ServiceID: "trade_owner", Address: string(address), ServicePath: "trpc.moox.trade.TradeConsoleService", AllowedMethods: []string{"GetLogicalAccount", "ClaimLogicalAccountOwner", "ReleaseLogicalAccountOwner", "RebindLogicalAccountOwner"}, AllowedCallers: []string{"strategy"}}})
	require.NoError(t, err)
	var table gatewayproxy.Table
	require.NoError(t, table.Replace(snapshot))
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	require.NoError(t, err)
	defer nonces.Close()
	gateway := httptest.NewTLSServer(New(Options{NodeID: "gateway-owner-e2e", Credentials: gatewayauth.CredentialsFromEnv(), MaxBodyBytes: 1 << 20, Table: &table, Nonces: nonces}))
	defer gateway.Close()
	require.NoError(t, os.WriteFile(filepath.Join(coord, "gateway-ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gateway.Certificate().Raw}), 0600))
	// A valid but unrelated CA proves trust rejection rather than PEM parse failure.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated-local-e2e-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	unrelatedDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(coord, "unrelated-ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: unrelatedDER}), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "gateway-ready"), []byte(gateway.URL), 0600))
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(coord, "strategy-done")); return e == nil }, 45*time.Second, 25*time.Millisecond)
	t.Log("real TLS Gateway handler, credential verification, persistent nonce consumption, route table and Forward exercised")
}

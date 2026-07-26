package cloudcredential

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/encoding/protojson"
)

type TencentCredential struct {
	SecretID  string
	SecretKey string
}

// Resolver resolves cloud account references through SecretMgr on every operation.
type Resolver struct {
	reveal func(context.Context, *adminpb.RevealSecretReq) (*adminpb.RevealSecretRsp, error)
}

func NewFromEnv() (*Resolver, error) {
	targetNode := gatewayauth.ServiceGatewayNodeID()
	credentials := gatewayauth.CredentialsFromEnv()
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_HTTP_URL")), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11002"
	}
	if targetNode == "" || credentials.KeyID == "" || credentials.Secret == "" || credentials.Caller == "" {
		return nil, fmt.Errorf("cloud credential resolver requires gateway HTTP URL, target node, key id, caller and service secret")
	}
	client, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: 10 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("create cloud credential gateway client: %w", err)
	}
	return &Resolver{reveal: func(ctx context.Context, req *adminpb.RevealSecretReq) (*adminpb.RevealSecretRsp, error) {
		return revealSecret(ctx, client, baseURL, targetNode, credentials, req)
	}}, nil
}

func revealSecret(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	targetNode string,
	credentials gatewayauth.Credentials,
	req *adminpb.RevealSecretReq,
) (*adminpb.RevealSecretRsp, error) {
	body, err := protojson.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal reveal secret request: %w", err)
	}
	const path = "/api/service/secret/RevealSecret"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create reveal secret request: %w", err)
	}
	headers, err := gatewayauth.Sign(credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: path, Body: body,
		TargetNode: targetNode, Caller: credentials.Caller,
	}, time.Now())
	if err != nil {
		return nil, fmt.Errorf("sign reveal secret request: %w", err)
	}
	httpReq.Header = headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpRsp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send reveal secret request: %w", err)
	}
	defer httpRsp.Body.Close()
	if httpRsp.StatusCode < 200 || httpRsp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpRsp.Body, 4096))
		return nil, fmt.Errorf("reveal secret HTTP %d", httpRsp.StatusCode)
	}
	var rsp adminpb.RevealSecretRsp
	raw, err := io.ReadAll(io.LimitReader(httpRsp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read reveal secret response: %w", err)
	}
	if err := protojson.Unmarshal(raw, &rsp); err != nil {
		return nil, fmt.Errorf("decode reveal secret response: %w", err)
	}
	return &rsp, nil
}

func (r *Resolver) Resolve(ctx context.Context, account store.CloudAccount) (TencentCredential, error) {
	if r == nil || r.reveal == nil {
		return TencentCredential{}, fmt.Errorf("cloud credential resolver is not configured")
	}
	if account.Provider != "tencent" || strings.TrimSpace(account.CredentialSecretID) == "" {
		return TencentCredential{}, fmt.Errorf("cloud account requires tencent provider and credential_secret_id")
	}
	response, err := r.reveal(ctx, &adminpb.RevealSecretReq{SecretId: account.CredentialSecretID})
	if err != nil {
		return TencentCredential{}, fmt.Errorf("reveal cloud credential: %w", err)
	}
	if response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || response.GetSecret() == nil {
		return TencentCredential{}, fmt.Errorf("reveal cloud credential rejected")
	}
	secret := response.GetSecret()
	if secret.GetStatus() != "active" || secret.GetCategory() != "cloud" || secret.GetProvider() != "tencent" {
		return TencentCredential{}, fmt.Errorf("cloud credential must be active category=cloud provider=tencent")
	}
	if strings.TrimSpace(secret.GetKeyId()) == "" || secret.GetSecretValue() == "" {
		return TencentCredential{}, fmt.Errorf("cloud credential is incomplete")
	}
	return TencentCredential{SecretID: secret.GetKeyId(), SecretKey: secret.GetSecretValue()}, nil
}

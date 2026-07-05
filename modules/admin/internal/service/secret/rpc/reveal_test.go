package rpc

import (
	"context"
	"net/http"
	"testing"

	secret "github.com/mooyang-code/moox/modules/admin/internal/service/secret"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

type revealSecretFakeService struct {
	secret.Service
	gotSecretID string
	record      *model.Secret
}

func (f *revealSecretFakeService) GetSecret(ctx context.Context, secretID string) (*model.Secret, error) {
	f.gotSecretID = secretID
	return f.record, nil
}

func TestRevealSecretReturnsPlainValueForServiceCall(t *testing.T) {
	fake := &revealSecretFakeService{
		record: &model.Secret{
			SecretID:    "sec_binance",
			Name:        "binance-main",
			Category:    "exchange",
			Provider:    "binance",
			SecretType:  "api_key",
			KeyID:       "public-api-key",
			SecretValue: "plain-api-secret",
			ExtraConfig: `{"market_type":"swap"}`,
			Status:      "active",
		},
	}
	svc := NewService(fake)

	rsp, err := svc.RevealSecret(serviceAuthCtx(), &pb.RevealSecretReq{SecretId: "sec_binance"})
	if err != nil {
		t.Fatalf("RevealSecret returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("RevealSecret ret_info = %v", rsp.GetRetInfo())
	}
	if fake.gotSecretID != "sec_binance" {
		t.Fatalf("GetSecret called with %q, want sec_binance", fake.gotSecretID)
	}
	if got := rsp.GetSecret().GetSecretValue(); got != "plain-api-secret" {
		t.Fatalf("secret_value = %q, want plaintext", got)
	}
	if got := rsp.GetSecret().GetKeyId(); got != "public-api-key" {
		t.Fatalf("key_id = %q", got)
	}
}

func TestRevealSecretRejectsInactiveSecret(t *testing.T) {
	svc := NewService(&revealSecretFakeService{
		record: &model.Secret{
			SecretID:    "sec_disabled",
			Name:        "disabled",
			Category:    "exchange",
			Provider:    "binance",
			SecretType:  "api_key",
			KeyID:       "key",
			SecretValue: "secret",
			Status:      "inactive",
		},
	})

	rsp, err := svc.RevealSecret(serviceAuthCtx(), &pb.RevealSecretReq{SecretId: "sec_disabled"})
	if err != nil {
		t.Fatalf("RevealSecret returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("RevealSecret code = %v, want INVALID_PARAM", rsp.GetRetInfo().GetCode())
	}
}

func TestRevealSecretRejectsControlPlaneCall(t *testing.T) {
	svc := NewService(&revealSecretFakeService{
		record: &model.Secret{
			SecretID:    "sec_binance",
			Name:        "binance-main",
			Category:    "exchange",
			Provider:    "binance",
			SecretType:  "api_key",
			KeyID:       "key",
			SecretValue: "secret",
			Status:      "active",
		},
	})

	rsp, err := svc.RevealSecret(context.Background(), &pb.RevealSecretReq{SecretId: "sec_binance"})
	if err != nil {
		t.Fatalf("RevealSecret returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("RevealSecret code = %v, want INVALID_PARAM", rsp.GetRetInfo().GetCode())
	}
}

func TestRevealSecretMapsMissingSecret(t *testing.T) {
	svc := NewService(&revealSecretFakeService{
		Service: secret.NewService(&dao.SecretDAO{}),
	})

	rsp, err := svc.RevealSecret(context.Background(), &pb.RevealSecretReq{})
	if err != nil {
		t.Fatalf("RevealSecret returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("RevealSecret code = %v, want INVALID_PARAM", rsp.GetRetInfo().GetCode())
	}
}

func serviceAuthCtx() context.Context {
	req := &http.Request{Header: http.Header{}}
	req.Header.Set("X-Moox-Service-Auth", "true")
	return thttp.WithHeader(context.Background(), &thttp.Header{Request: req})
}

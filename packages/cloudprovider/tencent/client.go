package tencent

import (
	"errors"
	"net/http"
	"strings"

	tencenterrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
)

var (
	ErrCredentialsInvalid = errors.New("tencent_credentials_invalid")
	ErrAuthentication     = errors.New("tencent_auth_failed")
	ErrRequestFailed      = errors.New("tencent_request_failed")
)

type Credentials struct {
	SecretID  string
	SecretKey string
}

func (c Credentials) validate() error {
	if strings.TrimSpace(c.SecretID) == "" || c.SecretKey == "" {
		return ErrCredentialsInvalid
	}
	return nil
}

func sanitizedSDKError(err error) error {
	var sdkErr *tencenterrors.TencentCloudSDKError
	if !errors.As(err, &sdkErr) {
		return ErrRequestFailed
	}
	code := sdkErr.GetCode()
	requestID := sdkErr.GetRequestId()
	base := ErrRequestFailed
	if strings.HasPrefix(strings.ToLower(code), "authfailure") || strings.Contains(strings.ToLower(code), "accesskey") {
		base = ErrAuthentication
	}
	if requestID == "" {
		return base
	}
	return &providerError{kind: base, requestID: requestID}
}

type providerError struct {
	kind      error
	requestID string
}

func (e *providerError) Error() string { return e.kind.Error() + ": request_id=" + e.requestID }
func (e *providerError) Unwrap() error { return e.kind }

func httpTransport(client *http.Client) http.RoundTripper {
	if client == nil || client.Transport == nil {
		return http.DefaultTransport
	}
	return client.Transport
}

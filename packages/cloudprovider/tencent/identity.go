package tencent

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	cloudprovider "github.com/mooyang-code/moox/packages/cloudprovider"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	sts "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/sts/v20180813"
)

type IdentityOptions struct {
	Credentials Credentials
	Endpoint    string
	HTTPClient  *http.Client
}

type identityValidator struct {
	client *sts.Client
}

const defaultIdentityRegion = "ap-guangzhou"

func NewIdentityValidator(options IdentityOptions) (cloudprovider.IdentityValidator, error) {
	if err := options.Credentials.validate(); err != nil {
		return nil, err
	}
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.ReqTimeout = 15
	if endpoint := strings.TrimSpace(options.Endpoint); endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			return nil, ErrRequestFailed
		}
		clientProfile.HttpProfile.Scheme = strings.ToUpper(parsed.Scheme)
		clientProfile.HttpProfile.Endpoint = parsed.Host
	}
	credential := common.NewCredential(strings.TrimSpace(options.Credentials.SecretID), options.Credentials.SecretKey)
	client, err := sts.NewClient(credential, defaultIdentityRegion, clientProfile)
	if err != nil {
		return nil, ErrRequestFailed
	}
	client.WithHttpTransport(httpTransport(options.HTTPClient))
	return &identityValidator{client: client}, nil
}

func (v *identityValidator) GetCallerIdentity(ctx context.Context) (cloudprovider.CallerIdentity, error) {
	response, err := v.client.GetCallerIdentityWithContext(ctx, sts.NewGetCallerIdentityRequest())
	if err != nil {
		return cloudprovider.CallerIdentity{}, sanitizedSDKError(err)
	}
	if response == nil || response.Response == nil || response.Response.AccountId == nil {
		return cloudprovider.CallerIdentity{}, ErrRequestFailed
	}
	identity := cloudprovider.CallerIdentity{
		Provider:  "tencent",
		AccountID: pointerString(response.Response.AccountId),
		RequestID: pointerString(response.Response.RequestId),
	}
	if identity.AccountID == "" {
		return cloudprovider.CallerIdentity{}, ErrRequestFailed
	}
	return identity, nil
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

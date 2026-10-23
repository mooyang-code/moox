package rpc

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

type fakeCORS struct {
	rules   []cos.BucketCORSRule
	getErr  error
	putErrs []error
	puts    int
}

func (f *fakeCORS) GetCORS(context.Context) (*cos.BucketGetCORSResult, *cos.Response, error) {
	if f.getErr != nil {
		return nil, nil, f.getErr
	}
	return &cos.BucketGetCORSResult{Rules: f.rules}, nil, nil
}

func (f *fakeCORS) PutCORS(_ context.Context, opt *cos.BucketPutCORSOptions) (*cos.Response, error) {
	f.puts++
	if len(f.putErrs) > 0 {
		err := f.putErrs[0]
		f.putErrs = f.putErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	f.rules = opt.Rules
	return nil, nil
}

// 首次配置失败后重试（桶已存在）必须补上 CORS；已有允许 PUT 的规则时不改动。
func TestEnsureBucketCORSRetriesAfterFailureAndKeepsExistingRules(t *testing.T) {
	notFound := &cos.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}, Code: "NoSuchCORSConfiguration"}
	api := &fakeCORS{getErr: notFound, putErrs: []error{errors.New("network")}}
	require.Error(t, ensureBucketCORS(context.Background(), api, "bucket"), "首次配置失败要报错")
	require.NoError(t, ensureBucketCORS(context.Background(), api, "bucket"), "重试时补上规则")
	require.Equal(t, 2, api.puts)
	require.Contains(t, api.rules[0].AllowedMethods, "PUT")

	existing := &fakeCORS{rules: []cos.BucketCORSRule{{AllowedMethods: []string{"put", "GET"}, AllowedOrigins: []string{"https://console.example"}}}}
	require.NoError(t, ensureBucketCORS(context.Background(), existing, "bucket"))
	require.Zero(t, existing.puts, "已有允许 PUT 的规则不改动")

	denied := &fakeCORS{getErr: errors.New("access denied")}
	require.Error(t, ensureBucketCORS(context.Background(), denied, "bucket"), "读取失败不能当作已配置")
}

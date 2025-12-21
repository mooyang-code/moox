package tencent

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
	"github.com/tencentyun/cos-go-sdk-v5"
)

// Provider 腾讯云Provider实现
type Provider struct {
	secretID    string
	secretKey   string
	scfClients  map[string]*scf.Client // key为region，支持多地区
	cosClient   *cos.Client
	region      string
	extraConfig map[string]interface{}
}

// Config 腾讯云配置
type Config struct {
	SecretID    string
	SecretKey   string
	Region      string
	COSBucket   string // COS桶名
	COSAppID    string // COS AppID
	ExtraConfig map[string]interface{}
}

// NewProvider 创建腾讯云Provider（内部使用）
func NewProvider(config *Config) (*Provider, error) {
	if config.SecretID == "" || config.SecretKey == "" {
		return nil, fmt.Errorf("secret id and secret key are required")
	}

	// 获取region配置，默认广州
	region := config.Region
	if region == "" {
		region = DefaultRegion
	}

	// 创建凭证
	credential := common.NewCredential(config.SecretID, config.SecretKey)

	// 定义所有腾讯云地区
	regions := []string{
		"ap-bangkok", "ap-beijing", "ap-chengdu", "ap-chongqing",
		"ap-guangzhou", "ap-hongkong", "ap-jakarta", "ap-nanjing",
		"ap-seoul", "ap-shanghai", "ap-shanghai-fsi", "ap-shenzhen-fsi",
		"ap-singapore", "ap-tokyo", "eu-frankfurt", "na-ashburn",
		"na-siliconvalley", "sa-saopaulo",
	}

	// 为每个地区创建 SCF 客户端
	scfClients := make(map[string]*scf.Client)
	for _, r := range regions {
		cpf := profile.NewClientProfile()
		cpf.HttpProfile.Endpoint = "scf.tencentcloudapi.com"
		cpf.HttpProfile.ReqTimeout = 240

		client, err := scf.NewClient(credential, r, cpf)
		if err != nil {
			return nil, fmt.Errorf("failed to create scf client for region %s: %w", r, err)
		}
		scfClients[r] = client
	}

	// 创建COS客户端（如果提供了COS配置），只创建一个默认广州
	var cosClient *cos.Client
	if config.COSBucket != "" && config.COSAppID != "" {
		cosRegion := DefaultRegion // 默认广州
		bucketURL, _ := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", config.COSBucket, cosRegion))
		baseURL := &cos.BaseURL{BucketURL: bucketURL}
		cosClient = cos.NewClient(baseURL, &http.Client{
			Transport: &cos.AuthorizationTransport{
				SecretID:  config.SecretID,
				SecretKey: config.SecretKey,
			},
		})
	}

	return &Provider{
		secretID:    config.SecretID,
		secretKey:   config.SecretKey,
		scfClients:  scfClients,
		cosClient:   cosClient,
		region:      region,
		extraConfig: config.ExtraConfig,
	}, nil
}

// GetSCFClient 获取指定地区的 SCF 客户端
func (p *Provider) GetSCFClient(region string) *scf.Client {
	if region == "" {
		region = p.region
	}
	if client, ok := p.scfClients[region]; ok {
		return client
	}
	// 降级到默认地区
	return p.scfClients[p.region]
}

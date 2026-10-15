package configinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// Keys 是用户填写的腾讯云访问密钥，只在内存里用于调用腾讯云接口。
type Keys struct {
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

func (k Keys) normalize() (Keys, error) {
	k.SecretID = strings.TrimSpace(k.SecretID)
	if err := checkText("腾讯云 SecretId", k.SecretID, true); err != nil {
		return Keys{}, err
	}
	if err := checkText("腾讯云 SecretKey", k.SecretKey, true); err != nil {
		return Keys{}, err
	}
	return k, nil
}

// Account 是密钥所属的腾讯云账号。
type Account struct {
	AccountID string `json:"account_id"`
}

// Instance 是按公网 IP 识别出的腾讯云服务器；规格字段取不到时为 0，页面不展示。
type Instance struct {
	Kind       string `json:"kind"`
	KindName   string `json:"kind_name"`
	Region     string `json:"region"`
	RegionName string `json:"region_name"`
	ZoneName   string `json:"zone_name"`
	Name       string `json:"name"`
	CPU        int    `json:"cpu,omitempty"`
	MemoryGB   int    `json:"memory_gb,omitempty"`
	DiskGB     int    `json:"disk_gb,omitempty"`
}

// errNotInAccount 表示这个 IP 不是当前腾讯云账号下的服务器。
var errNotInAccount = errors.New("not in account")

// Cloud 验证腾讯云账号、按公网 IP 识别服务器；测试里替换。
type Cloud interface {
	Verify(ctx context.Context, keys Keys) (Account, error)
	Lookup(ctx context.Context, keys Keys, address string) (Instance, error)
}

type tencentCloud struct{}

func (tencentCloud) Verify(ctx context.Context, keys Keys) (Account, error) {
	validator, err := tencent.NewIdentityValidator(tencent.IdentityOptions{Credentials: tencent.Credentials{SecretID: keys.SecretID, SecretKey: keys.SecretKey}})
	if err != nil {
		return Account{}, err
	}
	identity, err := validator.GetCallerIdentity(ctx)
	if err != nil {
		return Account{}, err
	}
	return Account{AccountID: identity.AccountID}, nil
}

func (tencentCloud) Lookup(ctx context.Context, keys Keys, address string) (Instance, error) {
	options := tencent.ClientOptions{SecretID: keys.SecretID, SecretKey: keys.SecretKey, Region: homeRegion}
	network, err := tencent.NewNetworkClient(options)
	if err != nil {
		return Instance{}, err
	}
	lighthouse, err := tencent.NewClient(options)
	if err != nil {
		return Instance{}, err
	}
	found, err := (privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}).LookupHost(ctx, address, tencent.ProbeRegions(homeRegion))
	if errors.Is(err, privatenet.ErrHostNotFound) {
		return Instance{}, errNotInAccount
	}
	if err != nil {
		return Instance{}, err
	}
	return describeInstance(found), nil
}

// homeRegion 是识别时最先查的地域；找不到再依次查其余地域。
const homeRegion = "ap-guangzhou"

func describeInstance(found tencent.CloudInstance) Instance {
	kind := "云服务器"
	if found.Kind == tencent.KindLighthouse {
		kind = "轻量应用服务器"
	}
	return Instance{
		Kind: found.Kind, KindName: kind, Region: found.Region, RegionName: regionName(found.Region),
		ZoneName: zoneName(found.Region, found.Zone), Name: found.InstanceName,
		CPU: found.CPU, MemoryGB: found.MemoryGB, DiskGB: found.DiskGB,
	}
}

func regionName(code string) string {
	for _, region := range tencent.SCFRegions() {
		if region.Code == code {
			return region.Name
		}
	}
	return code
}

// zoneName 把 ap-nanjing-1 显示成「南京一区」；认不出来时原样返回。
func zoneName(region, zone string) string {
	suffix, ok := strings.CutPrefix(zone, region+"-")
	digits := []string{"", "一", "二", "三", "四", "五", "六", "七", "八", "九"}
	if !ok || len(suffix) != 1 || suffix[0] < '1' || suffix[0] > '9' {
		if zone == "" {
			return regionName(region)
		}
		return zone
	}
	return strings.TrimPrefix(regionName(region), "中国") + digits[suffix[0]-'0'] + "区"
}

// cloudCache 记住本次会话里识别过的服务器，提交时不必再挨个地域查一遍。键里含密钥摘要，换了密钥就重新查。
type cloudCache struct {
	cloud Cloud
	mu    sync.Mutex
	found map[string]Instance
}

func newCloudCache(cloud Cloud) *cloudCache {
	return &cloudCache{cloud: cloud, found: map[string]Instance{}}
}

func (c *cloudCache) verify(ctx context.Context, keys Keys) (Account, error) {
	keys, err := keys.normalize()
	if err != nil {
		return Account{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	account, err := c.cloud.Verify(ctx, keys)
	if err != nil {
		return Account{}, fmt.Errorf("腾讯云密钥验证没有通过，请检查 SecretId 和 SecretKey 是否正确、是否已启用")
	}
	return account, nil
}

func (c *cloudCache) lookup(ctx context.Context, keys Keys, address string) (Instance, error) {
	keys, err := keys.normalize()
	if err != nil {
		return Instance{}, err
	}
	address = strings.TrimSpace(address)
	if ip := net.ParseIP(address); ip == nil || ip.To4() == nil || ip.IsPrivate() || ip.IsLoopback() {
		return Instance{}, fmt.Errorf("请填写服务器的公网 IPv4 地址")
	}
	sum := sha256.Sum256([]byte(keys.SecretID + "\x00" + keys.SecretKey + "\x00" + address))
	key := hex.EncodeToString(sum[:])
	c.mu.Lock()
	cached, ok := c.found[key]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	instance, err := c.cloud.Lookup(ctx, keys, address)
	if errors.Is(err, errNotInAccount) {
		return Instance{}, fmt.Errorf("%s 不在当前腾讯云账号下。请确认它是腾讯云服务器，并且和第一步填写的是同一个账号", address)
	}
	if err != nil {
		return Instance{}, fmt.Errorf("暂时查不到 %s 的信息，请稍后重试", address)
	}
	c.mu.Lock()
	c.found[key] = instance
	c.mu.Unlock()
	return instance, nil
}

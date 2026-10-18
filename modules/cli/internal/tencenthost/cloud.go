// Package tencenthost 按公网 IP 在腾讯云各地域里找到对应的轻量服务器或 CVM 实例。
package tencenthost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// ErrHostNotFound 表示在给定地域里都没找到这个公网 IP 对应的实例。
var ErrHostNotFound = errors.New("tencent instance not found")

type TencentCloud struct {
	Network    *tencent.NetworkClient
	Lighthouse *tencent.Client
}

func (c TencentCloud) LookupHost(ctx context.Context, publicIP string, regions []string) (tencent.CloudInstance, error) {
	var last error
	for _, region := range regions {
		if c.Lighthouse != nil {
			instance, found, err := c.Lighthouse.ForRegion(region).LookupInstance(ctx, publicIP)
			if err == nil && found {
				return instance, nil
			}
			if err != nil && !tencentIgnorable(err) {
				last = err
			}
		}
		if c.Network != nil {
			instance, found, err := c.Network.ForRegion(region).LookupCVM(ctx, publicIP)
			if err == nil && found {
				return instance, nil
			}
			if err != nil && !tencentIgnorable(err) {
				last = err
			}
			instance, found, err = c.Network.ForRegion(region).LookupCVMByEIP(ctx, publicIP)
			if err == nil && found {
				return instance, nil
			}
			if err != nil && !tencentIgnorable(err) {
				last = err
			}
		}
	}
	if last != nil {
		return tencent.CloudInstance{}, fmt.Errorf("lookup %s: %w", publicIP, last)
	}
	return tencent.CloudInstance{}, fmt.Errorf("%w for public ip %s", ErrHostNotFound, publicIP)
}

func tencentIgnorable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "authfailure") || strings.Contains(text, "secret") {
		return false
	}
	return strings.Contains(text, "region") ||
		strings.Contains(text, "unsupported") ||
		strings.Contains(text, "invalidparameter") ||
		strings.Contains(text, "resourcenotfound") ||
		strings.Contains(text, "not found")
}

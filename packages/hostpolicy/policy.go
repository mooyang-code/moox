// Package hostpolicy 定义每台主机上的策略缓存文件 <部署根>/data/host-agent/host-policy.json：全部告警渠道。
// HostAgent 定期从 Monitor 拉取最新值写入；HostAgent 的对端检查与 Admin 的证书巡检只读这个文件，不联网。
package hostpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// Policy 是策略缓存的内容。
type Policy struct {
	// Version 由写入方单调递增；ContentHash 是渠道的摘要，内容不变时不重写文件。
	Version     int64     `json:"version"`
	ContentHash string    `json:"content_hash"`
	FetchedAt   time.Time `json:"fetched_at"`
	// NotificationChannels 是全部告警渠道（与 Monitor 的通知渠道一致），告警发给每一个。
	NotificationChannels []Channel `json:"notification_channels"`
}

// Channel 是一个告警渠道：企业微信、飞书、钉钉群机器人或通用 Webhook。
// Secret 是渠道密钥（飞书的签名校验密钥、钉钉的加签密钥、通用 Webhook 的签名密钥），没有时为空；它参与摘要，改了密钥会重新下发。
type Channel struct {
	Type       string `json:"type"`
	WebhookURL string `json:"webhook_url"`
	Secret     string `json:"secret,omitempty"`
}

// channelTypes 与 packages/notification 支持的渠道类型一致。
var channelTypes = []string{"wecom", "feishu", "dingtalk", "webhook"}

// Validate 检查渠道是否可用。
func (p Policy) Validate() error {
	for i, channel := range p.NotificationChannels {
		if !slices.Contains(channelTypes, channel.Type) {
			return fmt.Errorf("channel %d: unknown type %q", i, channel.Type)
		}
		if channel.WebhookURL == "" {
			return fmt.Errorf("channel %d: webhook_url is required", i)
		}
	}
	return nil
}

// Hash 计算渠道的摘要（渠道按类型与地址排序，顺序不影响摘要）。
func (p Policy) Hash() string {
	channels := append([]Channel(nil), p.NotificationChannels...)
	sort.Slice(channels, func(i, j int) bool {
		if channels[i].Type != channels[j].Type {
			return channels[i].Type < channels[j].Type
		}
		return channels[i].WebhookURL < channels[j].WebhookURL
	})
	raw, _ := json.Marshal(struct {
		Channels []Channel `json:"channels"`
	}{channels})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Read 读取策略缓存。文件必须是当前用户的普通私密文件（Unix 上 0600），里面有推送凭据。
func Read(path string) (Policy, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Policy{}, err
	}
	if !info.Mode().IsRegular() {
		return Policy{}, errors.New("host policy must be a regular file")
	}
	if err := checkPrivate(info); err != nil {
		return Policy{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	var policy Policy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return Policy{}, fmt.Errorf("parse host policy: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// Write 原子写入策略缓存（临时文件 + fsync + rename），文件权限 0600。会重新计算 ContentHash。
func Write(path string, policy Policy) error {
	policy.ContentHash = policy.Hash()
	if err := policy.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".host-policy-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Package hostpolicy 定义每台主机上的策略缓存文件 <部署根>/run/host-policy.json：告警渠道与主机守护参数。
// 部署时用 moox.toml 的 [notification] 写一份种子，之后由 HostAgent 定期从 Monitor 拉取最新值覆盖；
// moox-runtime watch 与 Admin 的证书巡检只读这个文件，不联网。设计见 docs/计划/主机守护与告警高可用执行计划.md 第 4.4 节。
package hostpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileName 是策略缓存在部署根 run 目录下的文件名。
const FileName = "host-policy.json"

// 来源。
const (
	SourceSeed    = "seed"    // 部署时用 moox.toml 写入
	SourceMonitor = "monitor" // HostAgent 从 Monitor 拉取
)

// Policy 是策略缓存的内容。
type Policy struct {
	// Version 由写入方单调递增；ContentHash 是渠道与守护参数的摘要，内容不变时不重写文件。
	Version     int64     `json:"version"`
	ContentHash string    `json:"content_hash"`
	Source      string    `json:"source"`
	FetchedAt   time.Time `json:"fetched_at"`
	// NotificationChannels 是全部告警渠道（与 Monitor 的通知渠道一致），告警发给每一个。
	NotificationChannels []Channel `json:"notification_channels"`
	Watchdog             Watchdog  `json:"watchdog"`
}

// Channel 是一个告警渠道：企业微信或飞书群机器人。
type Channel struct {
	Type       string `json:"type"`
	WebhookURL string `json:"webhook_url"`
}

// Watchdog 是主机守护参数，空值表示使用守护自己的默认值。时长用 Go 的时长写法，例如 "10m"。
type Watchdog struct {
	// Disabled 为 true 时守护只检查、记录与告警，不拉起也不重启（中心的总开关）。
	Disabled          bool   `json:"disabled,omitempty"`
	MergeWindow       string `json:"merge_window,omitempty"`
	CrashLoopFailures int    `json:"crash_loop_failures,omitempty"`
	CrashLoopWindow   string `json:"crash_loop_window,omitempty"`
	CrashLoopRetry    string `json:"crash_loop_retry,omitempty"`
	CrashLoopSlow     string `json:"crash_loop_slow,omitempty"`
	StableAfter       string `json:"stable_after,omitempty"`
	NotReadyAlert     string `json:"not_ready_alert,omitempty"`
}

// Duration 解析一个时长参数；空值返回 0（由调用方使用默认值）。
func Duration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return parsed, nil
}

// Validate 检查渠道与参数是否可用。
func (p Policy) Validate() error {
	if p.Source != SourceSeed && p.Source != SourceMonitor {
		return fmt.Errorf("unknown host policy source %q", p.Source)
	}
	for i, channel := range p.NotificationChannels {
		if channel.Type != "wecom" && channel.Type != "feishu" {
			return fmt.Errorf("channel %d: unknown type %q", i, channel.Type)
		}
		if channel.WebhookURL == "" {
			return fmt.Errorf("channel %d: webhook_url is required", i)
		}
	}
	for _, value := range []string{p.Watchdog.MergeWindow, p.Watchdog.CrashLoopWindow, p.Watchdog.CrashLoopRetry, p.Watchdog.CrashLoopSlow, p.Watchdog.StableAfter, p.Watchdog.NotReadyAlert} {
		if _, err := Duration(value); err != nil {
			return err
		}
	}
	if p.Watchdog.CrashLoopFailures < 0 {
		return errors.New("crash_loop_failures must not be negative")
	}
	return nil
}

// Hash 计算渠道与守护参数的摘要（渠道按类型与地址排序，顺序不影响摘要）。
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
		Watchdog Watchdog  `json:"watchdog"`
	}{channels, p.Watchdog})
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

// Path 返回部署根下的策略缓存路径。
func Path(deploymentRoot string) string {
	return filepath.Join(deploymentRoot, "run", FileName)
}

// Summary 是策略缓存的概要，不含推送地址，可以安全地打印或经 SSH 传回。
type Summary struct {
	Path        string    `json:"path"`
	Present     bool      `json:"present"`
	Version     int64     `json:"version,omitempty"`
	Source      string    `json:"source,omitempty"`
	FetchedAt   time.Time `json:"fetched_at,omitzero"`
	ContentHash string    `json:"content_hash,omitempty"`
	Channels    int       `json:"channels"`
	Error       string    `json:"error,omitempty"`
}

// Summarize 读取策略缓存并返回概要；文件不存在或不可用时写在 Present / Error 里，不返回错误。
func Summarize(path string) Summary {
	summary := Summary{Path: path}
	policy, err := Read(path)
	switch {
	case os.IsNotExist(err):
		return summary
	case err != nil:
		summary.Present, summary.Error = true, err.Error()
		return summary
	}
	summary.Present, summary.Version, summary.Source, summary.FetchedAt = true, policy.Version, policy.Source, policy.FetchedAt
	summary.ContentHash, summary.Channels = policy.ContentHash, len(policy.NotificationChannels)
	return summary
}

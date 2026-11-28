package deploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// InstallCompleteMarker 记录一次完整的 setup install 验收通过后的完成标记，单独保存在控制机部署根下。
type InstallCompleteMarker struct {
	CompletedAt string `json:"completed_at"`
	SpaceID     string `json:"space_id,omitempty"`
	Version     string `json:"version,omitempty"`
	Commit      string `json:"commit,omitempty"`
}

// WriteInstallComplete 在控制机部署根下原子写出 install-complete.json（0600），
// 供 deployscan 区分“没装过 / 没装完 / 已装好”。
func WriteInstallComplete(ctx context.Context, target setupssh.Client, layout Layout, marker InstallCompleteMarker) error {
	if target == nil {
		return errors.New("写安装完成标记需要 SSH 连接")
	}
	if layout.Root == "" {
		return errors.New("写安装完成标记需要部署根路径")
	}
	if marker.CompletedAt == "" {
		marker.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("编码安装完成标记失败：%w", err)
	}
	raw = append(raw, '\n')

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := path.Join(layout.Root, "."+InstallCompleteFile+".tmp-"+hex.EncodeToString(suffix))
	if err := target.Upload(ctx, bytes.NewReader(raw), int64(len(raw)), tmp, 0o600); err != nil {
		return fmt.Errorf("上传安装完成标记失败：%w", err)
	}
	if output, err := target.Run(ctx, []string{"mv", "-f", "--", tmp, layout.InstallComplete()}, nil); err != nil {
		_, _ = target.Run(context.WithoutCancel(ctx), []string{"rm", "-f", "--", tmp}, nil)
		return fmt.Errorf("落盘安装完成标记失败：%s", lastLine(output.Stderr))
	}
	return nil
}

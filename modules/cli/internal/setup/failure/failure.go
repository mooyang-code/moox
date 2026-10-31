// Package failure 让目标机上的 moox-runtime 把失败原因以固定错误码带回操作机。
//
// 目标端的输出可能含私有内容，所以 CLI 默认隐藏；错误码是固定文本，只含白名单内的值，
// 操作机据此显示中文原因和下一步，不需要调试开关，也不用手动上机查看。
package failure

import (
	"errors"
	"fmt"
	"strings"
)

// Prefix 是目标端 stderr 里错误码行的开头。
const Prefix = "MOOX_ERROR_CODE="

// 目标端会返回的错误码。
const (
	TargetSubsetInstalled = "target_subset_installed"
	TargetIdentityDiffers = "target_identity_differs"
	TargetBindingMissing  = "target_binding_missing"
	TargetPartialInstall  = "target_partial_install"
	TargetDirectory       = "target_directory_invalid"
)

const nextStep = "不要重试，也不要手动上机处理；运行 setup validate 查看这台机器的状态，再按 route 决定怎么做"

var explanations = map[string]string{
	TargetSubsetInstalled: "目标机已经装过完整的 MooX，不是空机。" + nextStep,
	TargetIdentityDiffers: "目标机上的部署身份和这台电脑不一致，部署是由别的电脑完成的。" + nextStep,
	TargetBindingMissing:  "目标机上有旧部署，但缺少身份绑定记录。" + nextStep,
	TargetPartialInstall:  "目标机上只有部分单元，是一次不完整的安装。" + nextStep,
	TargetDirectory:       "目标机的部署目录不可用：属主或权限不符，或者上级目录不可写。运行 setup validate 查看具体原因",
}

// Coded 是带错误码的目标端错误，文本和错误码都不含动态内容。
type Coded struct {
	Code    string
	Message string
}

func (e *Coded) Error() string { return e.Message }

// New 创建带错误码的错误。
func New(code, message string) *Coded { return &Coded{Code: code, Message: message} }

// Line 返回写到 stderr 的错误码行；错误不带错误码时返回 false。
func Line(err error) (string, bool) {
	var coded *Coded
	if !errors.As(err, &coded) {
		return "", false
	}
	return Prefix + coded.Code, true
}

// Describe 把一次远端调用的失败转成给操作员看的错误。
// exitCode 为 0 表示没有拿到远端退出状态（SSH 连接断开），只有这种情况才用 retryHint 提示重试；
// 远端明确失败时只显示白名单里的原因，其余输出继续隐藏。
func Describe(retryHint string, exitCode int, stderr string) error {
	if exitCode == 0 {
		return errors.New(retryHint)
	}
	for _, line := range strings.Split(stderr, "\n") {
		code, ok := strings.CutPrefix(strings.TrimSpace(line), Prefix)
		if !ok {
			continue
		}
		if explanation, known := explanations[code]; known {
			return fmt.Errorf("%s: %s", code, explanation)
		}
	}
	operation, _, _ := strings.Cut(retryHint, ";")
	return fmt.Errorf("target_rejected: %s: 目标机执行失败，详细输出已隐藏。%s", operation, nextStep)
}

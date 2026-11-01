// Package failure 让目标机上的 moox-runtime 把失败原因以固定错误码带回操作机。
//
// 目标端的输出可能含私有内容，所以 CLI 默认隐藏；只有白名单内的错误码和字符受限的详情能带回来，
// 操作机据此显示中文原因和下一步，不需要调试开关，也不用手动上机查看。
package failure

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Prefix 是目标端 stderr 里错误码行的开头，DetailPrefix 是详情行的开头。
const (
	Prefix       = "MOOX_ERROR_CODE="
	DetailPrefix = "MOOX_ERROR_DETAIL="
)

// 目标端能明确说明原因的失败。
const (
	TargetSubsetInstalled = "target_subset_installed"
	TargetIdentityDiffers = "target_identity_differs"
	TargetBindingMissing  = "target_binding_missing"
	TargetPartialInstall  = "target_partial_install"
	TargetDirectory       = "target_directory_invalid"
	TargetNotReady        = "target_not_ready"
	TargetProcessExited   = "target_process_exited"
	TargetPortInUse       = "target_port_in_use"
)

const nextStep = "不要重试，也不要手动上机处理；运行 setup validate 查看这台机器的状态，再按 route 决定怎么做"

const inspectStep = "用 setup logs 看日志；配置和前置步骤都没变就不要重试"

// Explanations 是错误码的说明，skill 的处理表要逐项对应。
var Explanations = map[string]string{
	TargetSubsetInstalled: "目标机已经装过完整的 MooX，不是空机。" + nextStep,
	TargetIdentityDiffers: "目标机上的部署身份和这台电脑不一致，部署是由别的电脑完成的。" + nextStep,
	TargetBindingMissing:  "目标机上有旧部署，但缺少身份绑定记录。" + nextStep,
	TargetPartialInstall:  "目标机上只有部分单元，是一次不完整的安装。" + nextStep,
	TargetDirectory:       "目标机的部署目录不可用：属主或权限不符，或者上级目录不存在且不可写。" + nextStep,
	TargetNotReady:        "组件启动了，但在时限内没有就绪，已被停掉。" + inspectStep,
	TargetProcessExited:   "组件启动后自己退出了。" + inspectStep,
	TargetPortInUse:       "组件要用的端口被其他程序占用。先和用户确认能否停掉那个程序，不要手动杀进程",
}

// Coded 是带错误码的错误；Message 只留在目标端，Detail 经字符校验后带回操作机。
type Coded struct {
	Code    string
	Message string
	Detail  string
}

func (e *Coded) Error() string { return e.Message }

// New 创建带错误码的错误。
func New(code, message string) *Coded { return &Coded{Code: code, Message: message} }

// WithDetail 创建带错误码和详情的错误。详情只放组件名、端口、字段名这类公开信息。
func WithDetail(code, message, detail string) *Coded {
	return &Coded{Code: code, Message: message, Detail: detail}
}

// safeDetail 限定详情的字符：自由文本可能带路径或密钥，放不进来。
var safeDetail = regexp.MustCompile(`^[A-Za-z0-9_.,:=/ -]{1,400}$`)

// Line 返回写到 stderr 的错误码行，有详情时多一行；错误不带错误码时返回 false。
func Line(err error) (string, bool) {
	var coded *Coded
	if !errors.As(err, &coded) {
		return "", false
	}
	line := Prefix + coded.Code
	if safeDetail.MatchString(coded.Detail) {
		line += "\n" + DetailPrefix + coded.Detail
	}
	return line, true
}

// Describe 把一次远端调用的失败转成给操作员看的错误。operation 是这次操作的名字。
// exitCode 为 0 表示没有拿到远端退出状态（SSH 连接断开），这种情况目标机上的操作可能还在运行，不能算目标机失败。
// 远端明确失败时只显示白名单里的原因，其余输出继续隐藏，文案里不出现“断开”，避免被当成连接问题重试。
func Describe(operation string, exitCode int, stderr string) error {
	if exitCode == 0 {
		return Disconnected(operation)
	}
	code, detail := "", ""
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, Prefix); ok {
			if _, known := Explanations[value]; known && code == "" {
				code = value
			}
		} else if value, ok := strings.CutPrefix(line, DetailPrefix); ok && safeDetail.MatchString(value) && detail == "" {
			detail = value
		}
	}
	if code == "" {
		return fmt.Errorf("target_rejected: %s：目标机执行失败，详细输出已隐藏。%s", operation, nextStep)
	}
	if detail != "" {
		return fmt.Errorf("%s: %s（%s）。%s", code, operation, detail, Explanations[code])
	}
	return fmt.Errorf("%s: %s。%s", code, operation, Explanations[code])
}

// Disconnected 是 SSH 连接断开的错误。只有带这个错误码时，才可以用同样的参数重跑。
func Disconnected(operation string) error {
	return fmt.Errorf("ssh_disconnected: %s：SSH 连接断开，没有拿到结果，目标机上的操作可能还在运行。运行 setup validate，确认上一次操作已结束后，用同样的参数重跑", operation)
}

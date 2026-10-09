// Package tradeowner 负责向 Trade 认领、释放与校验组合账户的会话所有权。
// 只保留实例/会话这一套身份；订单、资金与持仓都不经过这里。
package tradeowner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/client"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

// DefaultTimeout 是 Trade 调用的默认超时。
const DefaultTimeout = 3 * time.Second

// Config 是 Trade 网关的接线配置；GatewayURL 与 TargetNode 为空表示未接线（只允许观察实例）。
type Config struct {
	GatewayURL string        `yaml:"gateway_url"`
	TargetNode string        `yaml:"target_node"`
	CAFile     string        `yaml:"ca_file"`
	Timeout    time.Duration `yaml:"timeout"`
}

// Validate 校验配置：loopback 以外必须 HTTPS，节点名须符合网关注册规则。
func (c Config) Validate() error {
	if c.Timeout <= 0 {
		return errors.New("trade timeout 必须大于 0")
	}
	if c.GatewayURL == "" && c.TargetNode == "" {
		return nil
	}
	if c.GatewayURL == "" || c.TargetNode == "" {
		return errors.New("trade gateway_url 与 target_node 必须同时配置")
	}
	if strings.ContainsFunc(c.TargetNode, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return errors.New("trade target_node 只能使用小写字母、数字、短横线和下划线")
	}
	u, err := url.Parse(c.GatewayURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("trade gateway_url 必须是不带凭据、路径、查询和片段的 origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (!strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback())) {
			return errors.New("trade gateway_url 除 loopback 外必须使用 HTTPS")
		}
	}
	return nil
}

// Configured 报告是否接线了 Trade 网关。
func (c Config) Configured() bool { return c.GatewayURL != "" && c.TargetNode != "" }

// Client 通过节点网关调用 Trade 的账户所有权接口。
type Client struct {
	proxy   logicalAccountProxy
	timeout time.Duration
}

// New 构造客户端；未接线时调用会返回配置错误。
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{proxy: newGateway(cfg), timeout: timeout}
}

// ResponseError 保留 Trade 的业务错误码，便于区分确定性的拒绝与结果未知的传输错误。
type ResponseError struct {
	Operation string
	Code      tradepb.ErrorCode
	Message   string
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "Trade 账户请求失败"
	}
	switch e.Code {
	case 5:
		return fmt.Sprintf("Trade 账户%s失败：账户不存在（code=5）", e.Operation)
	case 14:
		return fmt.Sprintf("Trade 账户%s失败：账户已由其他实例或会话持有（code=14）", e.Operation)
	default:
		return fmt.Sprintf("Trade 账户%s失败：Trade 拒绝了请求（code=%d）", e.Operation, e.Code)
	}
}

// Detail 返回错误链里 Trade 业务错误附带的原始说明（只写日志，不返回给接口调用方）；没有时返回空串。
func Detail(err error) string {
	var response *ResponseError
	if errors.As(err, &response) && response != nil {
		return strings.TrimSpace(response.Message)
	}
	return ""
}

// StatusCode 返回 Trade 的错误码。
func (e *ResponseError) StatusCode() int32 {
	if e == nil {
		return 0
	}
	return int32(e.Code)
}

// ErrSessionNotOwned 表示 Trade 账户当前的所有者不是该实例与会话。
var ErrSessionNotOwned = errors.New("Trade 账户的会话所有者与本实例不符")

// IsSessionRejected 判断会话校验失败是否是 Trade 的确定性结论：所有者已不是本会话，或账户不存在（错误码 5）。
// 传输失败、网关配置错误等结果未知的情况不算。
func IsSessionRejected(err error) bool {
	if errors.Is(err, ErrSessionNotOwned) {
		return true
	}
	var coded *ResponseError
	return errors.As(err, &coded) && coded.StatusCode() == 5
}

// IsReleaseSettled 判断释放失败是否已经是确定的结论：所有权不在本会话（冲突，错误码 14）或账户与空间不存在
// （错误码 5），都无需再释放。
func IsReleaseSettled(err error) bool {
	if IsOwnerConflict(err) {
		return true
	}
	var coded *ResponseError
	return errors.As(err, &coded) && coded.StatusCode() == 5
}

// IsOwnerConflict 判断错误是否为所有权冲突（Trade 错误码 14）。
func IsOwnerConflict(err error) bool {
	var coded *ResponseError
	return errors.As(err, &coded) && coded.StatusCode() == 14
}

// IsPermanentClaimError 判断认领失败是否为 Trade 的确定性拒绝：本地待定会话可以立即清除；
// 传输或内部错误则保留，让启动时的对账去恢复可能已经成功的远端结果。
func IsPermanentClaimError(err error) bool {
	var coded *ResponseError
	if !errors.As(err, &coded) {
		return false
	}
	switch coded.StatusCode() {
	case 1, 2, 3, 5, 6, 7, 8, 9, 10, 12, 13, 14, 16, 17, 18:
		return true
	default:
		return false
	}
}

// ClaimSession 以实例 + 会话身份认领账户所有权。
func (c *Client) ClaimSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error {
	if err := validateIdentity(spaceID, logicalAccountID, instanceID, sessionID); err != nil {
		return err
	}
	expectedFence, err := c.readAuthFence(ctx, spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.proxy.ClaimLogicalAccountOwner(callCtx, &tradepb.ClaimLogicalAccountOwnerReq{LogicalAccountId: logicalAccountID, InstanceId: instanceID, SessionId: sessionID, ExpectedAuthFence: expectedFence}, opts...)
	if err != nil {
		return c.transportError(callCtx, "认领会话", err)
	}
	if err := validateResponse("认领会话", spaceID, logicalAccountID, response.GetRetInfo(), response.GetLogicalAccount()); err != nil {
		return err
	}
	if response.GetLogicalAccount().GetOwnerInstanceId() != instanceID || response.GetLogicalAccount().GetOwnerSessionId() != sessionID {
		return errors.New("Trade 认领会话返回的所有者与请求不符")
	}
	return nil
}

// ReleaseSession 释放实例 + 会话持有的账户所有权。
func (c *Client) ReleaseSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error {
	if err := validateIdentity(spaceID, logicalAccountID, instanceID, sessionID); err != nil {
		return err
	}
	expectedFence, err := c.readAuthFence(ctx, spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.proxy.ReleaseLogicalAccountOwner(callCtx, &tradepb.ReleaseLogicalAccountOwnerReq{LogicalAccountId: logicalAccountID, InstanceId: instanceID, SessionId: sessionID, ExpectedAuthFence: expectedFence}, opts...)
	if err != nil {
		return c.transportError(callCtx, "释放会话", err)
	}
	return validateResponse("释放会话", spaceID, logicalAccountID, response.GetRetInfo(), response.GetLogicalAccount())
}

// ValidateSession 校验账户当前所有者仍是该实例与会话。
func (c *Client) ValidateSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error {
	if err := validateIdentity(spaceID, logicalAccountID, instanceID, sessionID); err != nil {
		return err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.proxy.GetLogicalAccount(callCtx, &tradepb.GetLogicalAccountReq{LogicalAccountId: logicalAccountID}, opts...)
	if err != nil {
		return c.transportError(callCtx, "校验会话", err)
	}
	if err := validateResponse("校验会话", spaceID, logicalAccountID, response.GetRetInfo(), response.GetLogicalAccount()); err != nil {
		return err
	}
	account := response.GetLogicalAccount()
	if account.GetOwnerInstanceId() != instanceID || account.GetOwnerSessionId() != sessionID {
		return ErrSessionNotOwned
	}
	return nil
}

func (c *Client) readAuthFence(ctx context.Context, spaceID, logicalAccountID string) (string, error) {
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return "", err
	}
	defer cancel()
	response, err := c.proxy.GetLogicalAccount(callCtx, &tradepb.GetLogicalAccountReq{LogicalAccountId: logicalAccountID}, opts...)
	if err != nil {
		return "", c.transportError(callCtx, "读取鉴权围栏", err)
	}
	if err := validateResponse("读取鉴权围栏", spaceID, logicalAccountID, response.GetRetInfo(), response.GetLogicalAccount()); err != nil {
		return "", err
	}
	return response.GetLogicalAccount().GetAuthFence(), nil
}

func (c *Client) call(ctx context.Context, spaceID string) (context.Context, context.CancelFunc, []client.Option, error) {
	if c == nil || c.proxy == nil {
		return nil, nil, nil, errors.New("Trade 账户客户端不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	callCtx, cancel := context.WithTimeout(context.WithValue(ctx, spaceKey{}, spaceID), timeout)
	reqHead := &thttp.ClientReqHeader{Header: make(http.Header)}
	reqHead.Header.Set("X-Space-Id", spaceID)
	return callCtx, cancel, []client.Option{client.WithReqHead(reqHead), client.WithTimeout(timeout)}, nil
}

// TransportError 表示与 Trade 通信失败、结果未知。Error 只给出中文概述；
// 原始错误可能包含网关地址，经 Unwrap 保留给日志，不返回给接口调用方。
type TransportError struct {
	Operation string
	Timeout   bool
	Err       error
}

func (e *TransportError) Error() string {
	if e.Timeout {
		return fmt.Sprintf("Trade 账户%s超时，结果未知", e.Operation)
	}
	return fmt.Sprintf("Trade 账户%s失败：Trade 或网关不可达，结果未知", e.Operation)
}

func (e *TransportError) Unwrap() error { return e.Err }

func (c *Client) transportError(ctx context.Context, operation string, err error) error {
	var visible visibleError
	if errors.As(err, &visible) {
		return fmt.Errorf("Trade 账户%s失败：%w", operation, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &TransportError{Operation: operation, Timeout: true, Err: errors.Join(ctxErr, err)}
	}
	return &TransportError{Operation: operation, Err: err}
}

func validateResponse(operation, spaceID, logicalAccountID string, retInfo *tradepb.RetInfo, account *tradepb.LogicalAccount) error {
	if retInfo == nil {
		return fmt.Errorf("Trade 账户 %s 没有返回状态", operation)
	}
	if retInfo.GetCode() != tradepb.ErrorCode_SUCCESS {
		return &ResponseError{Operation: operation, Code: retInfo.GetCode(), Message: retInfo.GetMsg()}
	}
	if account == nil {
		return fmt.Errorf("Trade 账户 %s 没有返回账户", operation)
	}
	if account.GetLogicalAccountId() != logicalAccountID || account.GetSpaceId() != spaceID {
		return fmt.Errorf("Trade 账户 %s 返回了不匹配的账户 %q（空间 %q）", operation, account.GetLogicalAccountId(), account.GetSpaceId())
	}
	return nil
}

func validateIdentity(spaceID, logicalAccountID, instanceID, sessionID string) error {
	if strings.TrimSpace(spaceID) == "" {
		return errors.New("账户所有权操作需要 space_id")
	}
	if strings.TrimSpace(logicalAccountID) == "" {
		return errors.New("账户所有权操作需要 logical_account_id")
	}
	if strings.TrimSpace(instanceID) == "" {
		return errors.New("账户所有权操作需要 instance_id")
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("账户所有权操作需要 session_id")
	}
	return nil
}

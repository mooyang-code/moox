// Package tradeowner 是 Strategy 调用 Trade 组合账户的两件事：启用绑了账户的实例前只读账户、核对控制模式与市场
// （GetLogicalAccount），停用实例后通知 Trade 关闭会话（CloseTargetSession）。实例与组合账户的归属只存在 Strategy，
// Trade 按目标事件带的会话先后接受目标；订单、资金与持仓都不经过这里。
package tradeowner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
)

// DefaultTimeout 是 Trade 调用的默认超时。
const DefaultTimeout = 3 * time.Second

// Config 是 Trade 调用的配置；调用经 gatewayclient（strategy 身份）发出，不再有网关地址。
type Config struct {
	Timeout time.Duration `yaml:"timeout"`
}

// Validate 校验配置。
func (c Config) Validate() error {
	if c.Timeout <= 0 {
		return errors.New("trade timeout 必须大于 0")
	}
	return nil
}

// logicalAccountProxy 是用到的 Trade 组合账户接口，便于测试替换。
type logicalAccountProxy interface {
	GetLogicalAccount(context.Context, *tradepb.GetLogicalAccountReq, ...client.Option) (*tradepb.GetLogicalAccountRsp, error)
	CloseTargetSession(context.Context, *tradepb.CloseTargetSessionReq, ...client.Option) (*tradepb.CloseTargetSessionRsp, error)
}

// Client 经 gatewayclient 调用 Trade 的组合账户接口。
type Client struct {
	proxy   logicalAccountProxy
	timeout time.Duration
}

// New 构造客户端；gateway 为空时客户端不可用，调用会返回错误。
func New(cfg Config, gateway gatewayclient.Invoker) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := &Client{timeout: timeout}
	if gateway != nil {
		client.proxy = &gatewayProxy{gateway: gateway}
	}
	return client
}

// ResponseError 保留 Trade 的业务错误码与原始说明（原始说明只写日志，不返回给接口调用方）。
type ResponseError struct {
	Operation string
	Code      tradepb.ErrorCode
	Message   string
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "Trade 账户请求失败"
	}
	if e.Code == tradepb.ErrorCode_NOT_FOUND {
		return fmt.Sprintf("Trade 账户%s失败：账户不存在（code=5）", e.Operation)
	}
	return fmt.Sprintf("Trade 账户%s失败：Trade 拒绝了请求（code=%d）", e.Operation, e.Code)
}

// Detail 返回错误链里 Trade 业务错误附带的原始说明（只写日志，不返回给接口调用方）；没有时返回空串。
func Detail(err error) string {
	var response *ResponseError
	if errors.As(err, &response) && response != nil {
		return strings.TrimSpace(response.Message)
	}
	return ""
}

// ErrMarketMismatch 表示策略的市场不在组合账户可交易的市场里，在启用时拦住，不让策略绑到没有对应市场的账户上。
var ErrMarketMismatch = errors.New("策略市场与组合账户市场不一致")

// ErrManualControl 表示组合账户是人工控制：Trade 不接受这种组合的策略目标，在启用时拦住。
var ErrManualControl = errors.New("组合账户是人工控制，不能绑定策略实例")

// CheckAccount 读一次组合账户（只读），核对它由策略控制，并且策略要输出的每个市场（spot、swap、equity）都在它可交易的市场里。
func (c *Client) CheckAccount(ctx context.Context, spaceID, logicalAccountID string, markets []string) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(logicalAccountID) == "" {
		return errors.New("读取组合账户需要 space_id 与 logical_account_id")
	}
	callCtx, cancel, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.proxy.GetLogicalAccount(callCtx, &tradepb.GetLogicalAccountReq{LogicalAccountId: logicalAccountID})
	if err != nil {
		return transportError(callCtx, "读取", err)
	}
	if err := checkRet("读取", response.GetRetInfo()); err != nil {
		return err
	}
	account := response.GetLogicalAccount()
	if account == nil {
		return errors.New("Trade 账户读取没有返回账户")
	}
	if account.GetLogicalAccountId() != logicalAccountID || account.GetSpaceId() != spaceID {
		return fmt.Errorf("Trade 账户读取返回了不匹配的账户 %q（空间 %q）", account.GetLogicalAccountId(), account.GetSpaceId())
	}
	if account.GetControlMode() != tradepb.ControlMode_CONTROL_MODE_STRATEGY {
		return fmt.Errorf("%w：组合账户 %s 的控制模式是 %s", ErrManualControl, logicalAccountID, account.GetControlMode())
	}
	return checkMarkets(markets, account.GetMarkets())
}

// CloseSession 在实例停用之后通知 Trade 关闭这个会话（幂等，批 3 的 3.6）：组合的“关闭到”推进到会话的开始时刻，开始时刻不大于它的
// 会话的目标都不再接受；这份目标的挂单由 Trade 的过期路径撤掉。Trade 回答账户不存在（code=5，例如 Trade 的数据被清空过）时视为已关闭：
// 这个组合上不会再有目标执行，不能让重复停用、删除实例永远卡在这里。
func (c *Client) CloseSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string, startedAt int64) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(logicalAccountID) == "" || strings.TrimSpace(instanceID) == "" || strings.TrimSpace(sessionID) == "" || startedAt <= 0 {
		return errors.New("关闭会话需要 space_id、logical_account_id、instance_id、session_id 与会话开始时刻")
	}
	callCtx, cancel, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.proxy.CloseTargetSession(callCtx, &tradepb.CloseTargetSessionReq{LogicalAccountId: logicalAccountID, InstanceId: instanceID, SessionId: sessionID, SessionStartedAt: startedAt})
	if err != nil {
		return transportError(callCtx, "关闭会话", err)
	}
	err = checkRet("关闭会话", response.GetRetInfo())
	var coded *ResponseError
	if errors.As(err, &coded) && coded.Code == tradepb.ErrorCode_NOT_FOUND {
		log.Warnf("Trade 里没有组合账户 %s（空间 %s），会话 %s/%s 视为已关闭：%s", logicalAccountID, spaceID, instanceID, sessionID, coded.Message)
		return nil
	}
	return err
}

// checkMarkets 检查策略要输出的每个市场（spot、swap、equity）都在组合账户可交易的市场里（启用成员已开通市场的并集）。
func checkMarkets(markets []string, accountMarkets []string) error {
	if len(accountMarkets) == 0 {
		return fmt.Errorf("%w：组合账户还没有启用的成员", ErrMarketMismatch)
	}
	if len(markets) == 0 {
		return fmt.Errorf("%w：策略没有要输出的市场", ErrMarketMismatch)
	}
	// 策略与 Trade 的市场写法一致（spot、swap、equity），直接比较。
	for _, market := range markets {
		if !slices.Contains(accountMarkets, market) {
			return fmt.Errorf("%w：策略要输出 %q，组合账户可交易的市场是 %v", ErrMarketMismatch, market, accountMarkets)
		}
	}
	return nil
}

func (c *Client) call(ctx context.Context, spaceID string) (context.Context, context.CancelFunc, error) {
	if c == nil || c.proxy == nil {
		return nil, nil, errors.New("Trade 账户客户端不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// 空间经调用元数据交给网关客户端：它决定转发给 Trade 的可信空间，不能由请求体自行声明。
	callCtx, cancel := context.WithTimeout(gatewayclient.WithCallMetadata(ctx, gatewayclient.CallMetadata{SpaceID: spaceID}), timeout)
	return callCtx, cancel, nil
}

// TransportError 表示与 Trade 通信失败、结果未知。Error 只给出中文概述；原始错误经 Unwrap 保留给日志，
// 不返回给接口调用方。
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

func transportError(ctx context.Context, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &TransportError{Operation: operation, Timeout: true, Err: errors.Join(ctxErr, err)}
	}
	return &TransportError{Operation: operation, Err: err}
}

func checkRet(operation string, retInfo *tradepb.RetInfo) error {
	if retInfo == nil {
		return fmt.Errorf("Trade 账户%s没有返回状态", operation)
	}
	if retInfo.GetCode() != tradepb.ErrorCode_SUCCESS {
		return &ResponseError{Operation: operation, Code: retInfo.GetCode(), Message: retInfo.GetMsg()}
	}
	return nil
}

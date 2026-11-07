package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var (
	// ErrInspectionBlocked：检测发现阻断项，账户不能建立（或已有账户不能继续下单）。
	ErrInspectionBlocked = errors.New("trade account: 密钥检测发现阻断项")
	// ErrInspectionUnavailable：这次检测没有成功（超时、限频、5xx），不下结论，请稍后重试。
	ErrInspectionUnavailable = errors.New("trade account: 密钥检测没有完成")
	// ErrReadOnlyCredential：只读密钥（录入页标记 readonly）不能用来建执行账户。
	ErrReadOnlyCredential = errors.New("trade account: 只读密钥不能用来建执行账户")
)

// Inspector 按场所检测交易所密钥；execution.Registry 实现它。
type Inspector interface {
	Inspect(context.Context, exchange.Exchange, exchange.InspectRequest) (exchange.Inspection, error)
}

// CredentialFromSecret 把 SecretMgr 里的交易所密钥转成适配器用的凭据；passphrase 等额外配置放在 extra_config（字符串键值）。
func CredentialFromSecret(secret ExchangeSecret) (exchange.Credential, error) {
	extra := map[string]string{}
	if strings.TrimSpace(secret.ExtraConfig) != "" {
		if err := json.Unmarshal([]byte(secret.ExtraConfig), &extra); err != nil {
			return exchange.Credential{}, fmt.Errorf("trade account: 解析密钥额外配置：%w", err)
		}
	}
	return exchange.Credential{
		APIKey: secret.KeyID, APISecret: secret.SecretValue,
		Passphrase: extra["passphrase"], Extra: extra,
	}, nil
}

// loadCredential 读取并核对一把交易所密钥（分类、交易所、状态），返回凭据和是否只读。
func (s *Service) loadCredential(ctx context.Context, name exchange.Exchange, secretID string) (exchange.Credential, bool, error) {
	if s.Secrets == nil {
		return exchange.Credential{}, false, ErrServiceNotConfigured
	}
	secret, err := s.Secrets.GetExchangeSecret(ctx, secretID)
	if err != nil {
		return exchange.Credential{}, false, err
	}
	if secret.SecretID != secretID || secret.Category != "exchange" || secret.Exchange != name || secret.Status != "active" {
		return exchange.Credential{}, false, ErrInvalidCredential
	}
	credential, err := CredentialFromSecret(secret)
	if err != nil {
		return exchange.Credential{}, false, err
	}
	return credential, credential.Extra["readonly"] == "true", nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// InspectCredential 只读检测一把密钥，不落库：录入密钥和建账户前预览。markets 为空时按这把密钥能开通的全部市场判断阻断项。
// 检测没有成功（传输错误）时返回 ErrInspectionUnavailable，调用方不能当作“有问题”。
func (s *Service) InspectCredential(
	ctx context.Context,
	name exchange.Exchange,
	environment exchange.AccountEnvironment,
	secretID string,
	markets []exchange.MarketType,
) (exchange.Inspection, error) {
	if s == nil || s.Inspector == nil {
		return exchange.Inspection{}, ErrServiceNotConfigured
	}
	credential, _, err := s.loadCredential(ctx, name, secretID)
	if err != nil {
		return exchange.Inspection{}, err
	}
	return s.inspect(ctx, name, exchange.InspectRequest{Environment: environment, Credential: credential, Markets: markets})
}

// inspectTimeout 是一次检测的总时限，要小于 TradeConsoleService 的服务端超时（config/trpc_go.yaml 的 15000ms）；
// 服务目录里两个检测方法的 method_timeouts_ms 与服务端超时一致（15000）。正常检测一两秒完成，到点按没有结论处理。
const inspectTimeout = 12 * time.Second

func (s *Service) inspect(ctx context.Context, name exchange.Exchange, request exchange.InspectRequest) (exchange.Inspection, error) {
	ctx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	inspection, err := s.Inspector.Inspect(ctx, name, request)
	if err != nil {
		if errors.Is(err, exchange.ErrInspectionInconclusive) || errors.Is(err, context.DeadlineExceeded) {
			// 错误文本会存库、返回前端，原始错误可能带签名后的请求串和交易所响应体，只留简短的中文说明。
			return exchange.Inspection{}, fmt.Errorf("%w：%s", ErrInspectionUnavailable, FailureSummary(err))
		}
		return exchange.Inspection{}, err
	}
	inspection.CredentialFingerprint = exchange.CredentialFingerprint(request.Credential.APIKey)
	return inspection, nil
}

// FailureSummary 把检测失败的错误转成简短的中文说明：类别、HTTP 状态和错误码，不含请求串和响应体原文。
func FailureSummary(err error) string {
	var typed *exchange.Error
	if !errors.As(err, &typed) && errors.Is(err, context.DeadlineExceeded) {
		return "检测超时"
	}
	return exchange.FailureSummary(err)
}

// inspectForSave 在建账户、换密钥、重新启用之前检测：检测没完成或有阻断项都不能保存；
// 通过后把账户类型和检测结果写进账户。只读密钥不能用来建执行账户。
func (s *Service) inspectForSave(ctx context.Context, value tradingaccount.Account) (tradingaccount.Account, error) {
	if value.ExecutionMode != exchange.ExecutionModeLive {
		return value, nil
	}
	if s.Inspector == nil {
		return tradingaccount.Account{}, ErrServiceNotConfigured
	}
	credential, readOnly, err := s.loadCredential(ctx, value.Exchange, value.CredentialSecretID)
	if err != nil {
		return tradingaccount.Account{}, err
	}
	if readOnly {
		return tradingaccount.Account{}, ErrReadOnlyCredential
	}
	inspection, err := s.inspect(ctx, value.Exchange, exchange.InspectRequest{
		Environment: value.Environment, Credential: credential, Markets: value.Markets,
	})
	if err != nil {
		return tradingaccount.Account{}, err
	}
	if blockers := inspection.BlockersFor(value.Markets); len(blockers) != 0 {
		return tradingaccount.Account{}, fmt.Errorf("%w：%s", ErrInspectionBlocked, joinFindings(blockers))
	}
	encoded, err := inspection.Encode()
	if err != nil {
		return tradingaccount.Account{}, err
	}
	value.AccountMode, value.InspectionJSON = inspection.AccountMode, encoded
	return value, nil
}

func joinFindings(findings []exchange.InspectionFinding) string {
	messages := make([]string, 0, len(findings))
	for _, finding := range findings {
		messages = append(messages, finding.Message)
	}
	return strings.Join(messages, "；")
}

// ReinspectResult 是对已有账户重新检测的结果。
type ReinspectResult struct {
	Inspection exchange.Inspection
	// PreviousMode 是检测前账户里的账户类型；ModeChanged 表示相比上次变了（上次为空不算变化）。
	PreviousMode exchange.AccountMode
	ModeChanged  bool
	// Blocked 表示按账户已开通的市场算出了阻断项。
	Blocked bool
	// Inconclusive 表示这次没有检测成功，保留了上次的结果，只累加失败次数。
	Inconclusive bool
	Cause        error
}

// Reinspect 对已有账户重新检测并把结果写回账户类型和检测结果。
// 传输错误（超时、限频、5xx）不下结论：保留上次结果，只累加连续失败次数，不触发阻断；调用方在次数到阈值时告警。
func (s *Service) Reinspect(ctx context.Context, accountID string) (ReinspectResult, error) {
	if s == nil || s.Store == nil || s.Inspector == nil {
		return ReinspectResult{}, ErrServiceNotConfigured
	}
	current, err := s.Store.Get(ctx, accountID)
	if err != nil {
		return ReinspectResult{}, err
	}
	if current.ExecutionMode != exchange.ExecutionModeLive {
		return ReinspectResult{}, fmt.Errorf("%w：只有实盘账户需要检测", tradingaccount.ErrInvalidAccount)
	}
	credential, _, err := s.loadCredential(ctx, current.Exchange, current.CredentialSecretID)
	if err != nil {
		return ReinspectResult{}, err
	}
	previous, _ := exchange.ParseInspection(current.InspectionJSON)
	result := ReinspectResult{PreviousMode: current.AccountMode}
	inspection, err := s.inspect(ctx, current.Exchange, exchange.InspectRequest{
		Environment: current.Environment, Credential: credential, Markets: current.Markets,
	})
	if err != nil {
		if !errors.Is(err, ErrInspectionUnavailable) {
			return ReinspectResult{}, err
		}
		previous.ConsecutiveFailures++
		previous.LastFailure, previous.LastFailureAt = truncateRunes(err.Error(), 200), s.now()
		encoded, encodeErr := previous.Encode()
		if encodeErr != nil {
			return ReinspectResult{}, encodeErr
		}
		if writeErr := s.Store.SetInspection(ctx, accountID, current.CredentialSecretID, current.AccountMode, encoded); writeErr != nil {
			return ReinspectResult{}, staleInspection(writeErr)
		}
		result.Inspection, result.Inconclusive, result.Cause = previous, true, err
		result.Blocked = previous.Blocked(current.Markets)
		return result, nil
	}
	// 交易所拒绝了密钥，或疑似统一账户旧密钥（papi、fapi 都被拒）：这次检测的结论是“密钥有问题”，不是“账户类型变了”，
	// 也没能读出账户类型。已有账户的类型沿用，否则会话会被换成另一种适配器、账户类型被清空；阻断项照常生效。
	if current.AccountMode != exchange.AccountModeUnspecified &&
		(inspection.CredentialRejected != "" || inspection.PortfolioMarginSuspected || inspection.AccountMode == exchange.AccountModeUnspecified) {
		inspection.AccountMode = current.AccountMode
	}
	encoded, err := inspection.Encode()
	if err != nil {
		return ReinspectResult{}, err
	}
	if err := s.Store.SetInspection(ctx, accountID, current.CredentialSecretID, inspection.AccountMode, encoded); err != nil {
		return ReinspectResult{}, staleInspection(err)
	}
	result.Inspection = inspection
	result.ModeChanged = current.AccountMode != exchange.AccountModeUnspecified && current.AccountMode != inspection.AccountMode
	result.Blocked = inspection.Blocked(current.Markets)
	return result, nil
}

// staleInspection 说明检测期间账户换了密钥：旧密钥的结果已丢弃，不覆盖新密钥的结果。
func staleInspection(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("%w：账户在检测期间换了密钥，这次检测结果已丢弃，请对新密钥重新检测", err)
	}
	return err
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

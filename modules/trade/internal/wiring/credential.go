package wiring

import (
	"context"
	"fmt"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// ExchangeCredential 从密钥服务读取一把交易所密钥并转成适配器凭据。
func ExchangeCredential(
	ctx context.Context,
	secrets accountapp.SecretSource,
	exchangeName exchange.Exchange,
	secretID string,
) (exchange.Credential, error) {
	value, err := secrets.GetExchangeSecret(ctx, secretID)
	if err != nil {
		return exchange.Credential{}, err
	}
	return CredentialFromSecret(value, exchangeName, secretID)
}

// CredentialFromSecret 校验交易所密钥的元数据，并转换成适配器凭据。
func CredentialFromSecret(
	value accountapp.ExchangeSecret,
	exchangeName exchange.Exchange,
	secretID string,
) (exchange.Credential, error) {
	if value.SecretID != secretID ||
		value.Exchange != exchangeName ||
		value.Category != "exchange" ||
		value.Status != "active" {
		return exchange.Credential{}, fmt.Errorf(
			"交易所密钥 %q 元数据不匹配",
			secretID,
		)
	}
	return accountapp.CredentialFromSecret(value)
}

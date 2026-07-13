package tradepb

import (
	"fmt"
	"strings"
)

func (r *CreateApiKeyReq) Validate() error {
	if r == nil || strings.TrimSpace(r.AccountId) == "" || strings.TrimSpace(r.Exchange) == "" {
		return fmt.Errorf("account_id and exchange are required")
	}
	if strings.TrimSpace(r.ApiKey) == "" || strings.TrimSpace(r.ApiSecret) == "" {
		return fmt.Errorf("api_key and api_secret are required")
	}
	return nil
}

func (r *TransferReq) Validate() error {
	if r == nil || strings.TrimSpace(r.FromAccountId) == "" || strings.TrimSpace(r.ToAccountId) == "" {
		return fmt.Errorf("from_account_id and to_account_id are required")
	}
	if strings.TrimSpace(r.Currency) == "" || strings.TrimSpace(r.Amount) == "" {
		return fmt.Errorf("currency and amount are required")
	}
	return nil
}

func (r *PlaceOrderReq) Validate() error {
	if r == nil || strings.TrimSpace(r.AccountId) == "" || strings.TrimSpace(r.ChannelId) == "" || strings.TrimSpace(r.Symbol) == "" {
		return fmt.Errorf("account_id, channel_id and symbol are required")
	}
	if strings.TrimSpace(r.Quantity) == "" && strings.TrimSpace(r.Amount) == "" {
		return fmt.Errorf("quantity or amount is required")
	}
	return nil
}

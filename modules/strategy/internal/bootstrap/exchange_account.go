package bootstrap

import (
	"context"
	"errors"
	"net/http"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/client"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

type exchangeAccountModeClient struct {
	client tradepb.ExchangeAccountServiceClientProxy
}

func newExchangeAccountModeClient(target string) *exchangeAccountModeClient {
	return &exchangeAccountModeClient{
		client: tradepb.NewExchangeAccountServiceClientProxy(
			client.WithTarget(target),
			client.WithNetwork("tcp"),
			client.WithProtocol("http"),
		),
	}
}

func (c *exchangeAccountModeClient) ExecutionMode(
	ctx context.Context,
	spaceID string,
	exchangeAccountID string,
) (string, error) {
	if c == nil || c.client == nil {
		return "", errors.New("Exchange account client is unavailable")
	}
	if spaceID == "" {
		return "", errors.New("binding space is required for Exchange account lookup")
	}
	reqHead := &thttp.ClientReqHeader{Header: make(http.Header)}
	reqHead.Header.Set("X-Space-Id", spaceID)
	response, err := c.client.GetAccount(
		ctx,
		&tradepb.GetAccountReq{ExchangeAccountId: exchangeAccountID},
		client.WithReqHead(reqHead),
	)
	if err != nil {
		return "", err
	}
	if response == nil || response.GetRetInfo().GetCode() != 0 || response.GetAccount() == nil {
		message := "Exchange account was not found"
		if response != nil && response.GetRetInfo().GetMsg() != "" {
			message = response.GetRetInfo().GetMsg()
		}
		return "", errors.New(message)
	}
	switch response.GetAccount().GetExecutionMode() {
	case tradepb.ExecutionMode_EXECUTION_MODE_PAPER:
		return "paper", nil
	case tradepb.ExecutionMode_EXECUTION_MODE_LIVE:
		return "live", nil
	default:
		return "", errors.New("Exchange account mode is unspecified")
	}
}

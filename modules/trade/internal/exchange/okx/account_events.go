package okx

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"golang.org/x/net/websocket"
)

func (a *Adapter) Subscribe(ctx context.Context, handler execution.AccountEventHandler) error {
	if handler == nil {
		return rejected("private event handler is required", nil)
	}
	config, err := websocket.NewConfig(
		privateStreamEndpoint(a.config.Environment),
		"https://moox.local",
	)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	login := map[string]any{"op": "login", "args": []map[string]string{{
		"apiKey": a.credential.APIKey, "passphrase": a.credential.Passphrase,
		"timestamp": timestamp,
		"sign":      signature(a.credential.APISecret, timestamp, "GET", "/users/self/verify", ""),
	}}}
	if err := websocket.JSON.Send(connection, login); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	if err := expectOKXAck(connection, "login"); err != nil {
		return err
	}
	channels := a.privateChannels()
	subscribe := map[string]any{"op": "subscribe", "args": channels}
	if err := websocket.JSON.Send(connection, subscribe); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	pending, err := expectOKXSubscribeAcks(connection, channels)
	if err != nil {
		return err
	}
	handler.OnSubscribed()
	for _, payload := range pending {
		if err := a.dispatchPrivate(ctx, payload, handler); err != nil {
			return err
		}
	}

	for {
		if err := a.receivePrivateMessage(ctx, connection, handler); err != nil {
			return err
		}
	}
}

func (a *Adapter) receivePrivateMessage(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
) error {
	return a.receivePrivateMessageWithTimeouts(
		ctx,
		connection,
		handler,
		25*time.Second,
		5*time.Second,
	)
}

func (a *Adapter) receivePrivateMessageWithTimeouts(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
	idleTimeout time.Duration,
	pongTimeout time.Duration,
) error {
	_ = connection.SetReadDeadline(time.Now().Add(idleTimeout))
	var payload []byte
	err := websocket.Message.Receive(connection, &payload)
	if err == nil {
		if string(payload) == "pong" {
			return nil
		}
		return a.dispatchPrivate(ctx, payload, handler)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	timeout, ok := err.(net.Error)
	if !ok || !timeout.Timeout() {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	_ = connection.SetWriteDeadline(time.Now().Add(pongTimeout))
	if err := websocket.Message.Send(connection, "ping"); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	_ = connection.SetReadDeadline(time.Now().Add(pongTimeout))
	for {
		if err := websocket.Message.Receive(connection, &payload); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
		}
		if string(payload) == "pong" {
			return nil
		}
		if err := a.dispatchPrivate(ctx, payload, handler); err != nil {
			return err
		}
	}
}

func expectOKXSubscribeAcks(
	connection *websocket.Conn,
	channels []map[string]string,
) ([][]byte, error) {
	pending := make(map[string]struct{}, len(channels))
	buffered := make([][]byte, 0)
	for _, channel := range channels {
		pending[channel["channel"]] = struct{}{}
	}
	for len(pending) > 0 {
		var raw []byte
		if err := websocket.Message.Receive(connection, &raw); err != nil {
			return nil, &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
		}
		var payload struct {
			Event string `json:"event"`
			Code  string `json:"code"`
			Msg   string `json:"msg"`
			Arg   struct {
				Channel string `json:"channel"`
			} `json:"arg"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, rejected("decode private subscription frame", err)
		}
		if payload.Event == "" && payload.Arg.Channel != "" {
			buffered = append(buffered, append([]byte(nil), raw...))
			continue
		}
		if payload.Event != "subscribe" ||
			(payload.Code != "" && payload.Code != "0") {
			return nil, classifyOKXCode(payload.Code, payload.Msg)
		}
		if _, found := pending[payload.Arg.Channel]; !found {
			return nil, rejected(
				"unexpected or duplicate private channel acknowledgement",
				nil,
			)
		}
		delete(pending, payload.Arg.Channel)
	}
	return buffered, nil
}

func (a *Adapter) privateChannels() []map[string]string {
	channels := []map[string]string{
		{"channel": "orders", "instType": a.instrumentType()},
		{"channel": "account"},
	}
	if a.config.MarketType == exchange.MarketTypeSwap {
		channels = append(channels, map[string]string{
			"channel": "positions", "instType": "SWAP",
		})
	}
	return channels
}

func expectOKXAck(connection *websocket.Conn, expected string) error {
	var payload struct {
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
	}
	if err := websocket.JSON.Receive(connection, &payload); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	if payload.Event != expected || (payload.Code != "" && payload.Code != "0") {
		return classifyOKXCode(payload.Code, payload.Msg)
	}
	return nil
}

type privateMessage struct {
	Arg struct {
		Channel string `json:"channel"`
	} `json:"arg"`
	Event string          `json:"event"`
	Code  string          `json:"code"`
	Msg   string          `json:"msg"`
	Data  json.RawMessage `json:"data"`
}

func (a *Adapter) dispatchPrivate(
	ctx context.Context,
	payload []byte,
	handler execution.AccountEventHandler,
) error {
	var message privateMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return rejected("decode private event", err)
	}
	if message.Event == "error" {
		return classifyOKXCode(message.Code, message.Msg)
	}
	// Login and subscribe acknowledgements do not carry channel data. OKX
	// emits one subscribe acknowledgement per requested channel.
	if message.Event != "" {
		return nil
	}
	switch message.Arg.Channel {
	case "orders":
		var rows []struct {
			orderPayload
			TradeID  string `json:"tradeId"`
			FillSz   string `json:"fillSz"`
			FillPx   string `json:"fillPx"`
			Fee      string `json:"fee"`
			FeeCcy   string `json:"feeCcy"`
			FillPnl  string `json:"fillPnl"`
			ExecType string `json:"execType"`
			FillTime string `json:"fillTime"`
		}
		if err := json.Unmarshal(message.Data, &rows); err != nil {
			return rejected("decode private orders", err)
		}
		for _, row := range rows {
			order, err := a.order(row.orderPayload)
			if err != nil {
				return err
			}
			if err := handler.OnOrder(ctx, order); err != nil {
				return err
			}
			if row.TradeID == "" || row.FillSz == "" || row.FillSz == "0" {
				continue
			}
			fill, err := a.fill(fillPayload{
				InstID: row.InstID, TradeID: row.TradeID,
				OrdID: row.OrdID, ClOrdID: row.ClOrdID,
				Side: row.Side, PosSide: row.PosSide,
				FillSz: row.FillSz, FillPx: row.FillPx,
				Fee: row.Fee, FeeCcy: row.FeeCcy,
				FillPnl: row.FillPnl, ExecType: row.ExecType, Ts: row.FillTime,
			})
			if err != nil {
				return err
			}
			if err := handler.OnFill(ctx, fill); err != nil {
				return err
			}
		}
	case "positions":
		var rows []positionPayload
		if err := json.Unmarshal(message.Data, &rows); err != nil {
			return rejected("decode private positions", err)
		}
		for _, row := range rows {
			if row.PosSide != "" && row.PosSide != "net" {
				return rejected("hedge position mode is unsupported", nil)
			}
			if row.MgnMode != "cross" {
				return rejected("isolated margin is unsupported", nil)
			}
			contracts, err := decimalOrZero(row.Pos)
			if err != nil {
				return err
			}
			quantity, err := a.toBaseQuantity(row.InstID, contracts)
			if err != nil {
				return err
			}
			position := exchange.Position{
				TradingAccountID: a.config.TradingAccountID,
				ExchangeSymbol:   row.InstID, Symbol: row.InstID, PositionSide: exchange.PositionSideNet,
				SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
				ExchangeUpdatedAt: millisString(row.UTime),
				Present: exchange.PositionPresence{
					SignedQuantity:   true,
					EntryPrice:       row.AvgPx != "",
					MarkPrice:        row.MarkPx != "",
					Leverage:         row.Lever != "",
					MarginMode:       true,
					UsedMargin:       row.IMR != "" || row.Margin != "",
					LiquidationPrice: row.LiqPx != "",
					UnrealizedPnL:    row.Upl != "",
					RealizedPnL:      row.Realized != "",
				},
			}
			position.RequiresSync = !position.Present.EntryPrice ||
				!position.Present.MarkPrice ||
				!position.Present.Leverage ||
				!position.Present.UsedMargin ||
				!position.Present.UnrealizedPnL
			position.EntryPrice, err = decimalOrZero(row.AvgPx)
			if err == nil {
				position.MarkPrice, err = decimalOrZero(row.MarkPx)
			}
			if err == nil {
				position.Leverage, err = decimalOrZero(row.Lever)
			}
			if err == nil {
				usedMargin := row.IMR
				if usedMargin == "" {
					usedMargin = row.Margin
				}
				position.UsedMargin, err = decimalOrZero(usedMargin)
			}
			if err == nil {
				position.LiquidationPrice, err = decimalOrZero(row.LiqPx)
			}
			if err == nil {
				position.UnrealizedPnL, err = decimalOrZero(row.Upl)
			}
			if err == nil {
				position.RealizedPnL, err = decimalOrZero(row.Realized)
			}
			if err != nil {
				return err
			}
			if err := handler.OnPosition(ctx, position); err != nil {
				return err
			}
		}
	case "account":
		var rows []balanceEnvelope
		if err := json.Unmarshal(message.Data, &rows); err != nil || len(rows) == 0 {
			return rejected("decode private account", err)
		}
		snapshot := exchange.AccountSnapshot{
			ExchangeUpdatedAt: millisString(rows[0].UTime),
			Present: exchange.AccountSnapshotPresence{
				Balances: true,
				Equity:   true,
			},
		}
		equity, err := decimalOrZero(rows[0].TotalEq)
		if err != nil {
			return err
		}
		snapshot.Equity = equity
		for _, detail := range rows[0].Details {
			totalRaw := detail.Eq
			if totalRaw == "" {
				totalRaw = detail.CashBal
			}
			total, err := decimalOrZero(totalRaw)
			if err != nil {
				return err
			}
			availableRaw := detail.AvailEq
			if availableRaw == "" {
				availableRaw = detail.AvailBal
			}
			available, err := decimalOrZero(availableRaw)
			if err != nil {
				return err
			}
			locked, err := decimalOrZero(detail.FrozenBal)
			if err != nil {
				return err
			}
			snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
				Asset: detail.Ccy, Available: available, Locked: locked, Total: total,
			})
			if strings.EqualFold(detail.Ccy, a.config.SettlementAsset) {
				snapshot.AvailableFunds = available
				snapshot.UsedMargin, err = decimalOrZero(detail.Imr)
				if err == nil {
					snapshot.MaintenanceMargin, err = decimalOrZero(detail.Mmr)
				}
				if err == nil {
					snapshot.UnrealizedPnL, err = decimalOrZero(detail.Upl)
				}
				if err != nil {
					return err
				}
				snapshot.Present.AvailableFunds = true
				snapshot.Present.UsedMargin = true
				snapshot.Present.MaintenanceMargin = true
				snapshot.Present.UnrealizedPnL = true
			}
		}
		snapshot.RequiresSync = !snapshot.Present.AvailableFunds
		if err := handler.OnAccountSnapshot(ctx, snapshot); err != nil {
			return err
		}
	}
	return nil
}

package okx

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"golang.org/x/net/websocket"
)

func (a *Adapter) SubscribePrivate(ctx context.Context, handler exchange.EventHandler) error {
	if handler == nil {
		return rejected("private event handler is required", nil)
	}
	config, err := websocket.NewConfig("wss://ws.okx.com:8443/ws/v5/private", "https://moox.local")
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
	subscribe := map[string]any{"op": "subscribe", "args": a.privateChannels()}
	if err := websocket.JSON.Send(connection, subscribe); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	if err := expectOKXAck(connection, "subscribe"); err != nil {
		return err
	}

	for {
		_ = connection.SetReadDeadline(time.Now().Add(25 * time.Second))
		var payload []byte
		if err := websocket.Message.Receive(connection, &payload); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if sendErr := websocket.Message.Send(connection, "ping"); sendErr == nil {
					continue
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
		}
		if string(payload) == "pong" {
			continue
		}
		if err := a.dispatchPrivate(ctx, payload, handler); err != nil {
			return err
		}
	}
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
	handler exchange.EventHandler,
) error {
	var message privateMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return rejected("decode private event", err)
	}
	if message.Event == "error" {
		return classifyOKXCode(message.Code, message.Msg)
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
			contracts, err := decimalOrZero(row.Pos)
			if err != nil {
				return err
			}
			quantity, err := a.toBaseQuantity(row.InstID, contracts)
			if err != nil {
				return err
			}
			position := exchange.Position{
				ExchangeAccountID: a.config.ExchangeAccountID,
				Symbol:            row.InstID, PositionSide: exchange.PositionSideNet,
				SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
				ExchangeUpdatedAt: millisString(row.UTime),
			}
			position.EntryPrice, err = decimalOrZero(row.AvgPx)
			if err == nil {
				position.MarkPrice, err = decimalOrZero(row.MarkPx)
			}
			if err == nil {
				position.Leverage, err = decimalOrZero(row.Lever)
			}
			if err == nil {
				position.UsedMargin, err = decimalOrZero(row.Margin)
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
		snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millisString(rows[0].UTime)}
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
		}
		if err := handler.OnAccountSnapshot(ctx, snapshot); err != nil {
			return err
		}
	}
	return nil
}

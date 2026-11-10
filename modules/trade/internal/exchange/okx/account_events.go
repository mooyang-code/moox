package okx

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"golang.org/x/net/websocket"
)

// privateHandshakeTimeout 是私有流拨号、登录与订阅确认阶段的统一期限。
var privateHandshakeTimeout = 15 * time.Second

// 私有流空闲多久发一次 ping，以及 ping 后等待 pong 的期限；测试里会改短。
var (
	privateIdleTimeout = 25 * time.Second
	privatePongTimeout = 5 * time.Second
)

func (a *Adapter) Subscribe(ctx context.Context, handler execution.AccountEventHandler) error {
	if handler == nil {
		return exchange.Rejected("private event handler is required", nil)
	}
	// 拨号（含 HTTP Upgrade）、登录与订阅确认共用一个继承调用方取消的期限
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, privateHandshakeTimeout)
	defer cancelHandshake()
	connection, err := dialPrivateStream(handshakeCtx, privateStreamEndpoint(a.config.Environment))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()

	pending, err := a.privateHandshake(connection)
	if err != nil {
		return err
	}
	_ = connection.SetDeadline(time.Time{})
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

// dialPrivateStream 建立私有流连接；ctx 同时约束 TCP/TLS 建连与 HTTP Upgrade 响应。
func dialPrivateStream(ctx context.Context, endpoint string) (*websocket.Conn, error) {
	config, err := websocket.NewConfig(endpoint, "https://moox.local")
	if err != nil {
		return nil, err
	}
	return config.DialContext(ctx)
}

// privateHandshake 完成登录与订阅确认，整个过程受 privateHandshakeTimeout 约束，返回确认前缓冲的数据帧。
func (a *Adapter) privateHandshake(connection *websocket.Conn) ([][]byte, error) {
	// 登录与订阅确认阶段设置统一期限：服务端保持连接但不应答时返回错误，由上层重连
	handshakeDeadline := time.Now().Add(privateHandshakeTimeout)
	_ = connection.SetDeadline(handshakeDeadline)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	login := map[string]any{"op": "login", "args": []map[string]string{{
		"apiKey": a.credential.APIKey, "passphrase": a.credential.Passphrase,
		"timestamp": timestamp,
		"sign":      signature(a.credential.APISecret, timestamp, "GET", "/users/self/verify", ""),
	}}}
	if err := websocket.JSON.Send(connection, login); err != nil {
		return nil, &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	if err := expectOKXAck(connection, "login"); err != nil {
		return nil, err
	}
	channels := a.privateChannels()
	subscribe := map[string]any{"op": "subscribe", "args": channels}
	if err := websocket.JSON.Send(connection, subscribe); err != nil {
		return nil, &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	return expectOKXSubscribeAcks(connection, channels)
}

// receivePrivateMessage 读一条私有流消息；空闲超过 privateIdleTimeout 就发 ping，pong 在 privatePongTimeout 内必须回来。
func (a *Adapter) receivePrivateMessage(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
) error {
	_ = connection.SetReadDeadline(time.Now().Add(privateIdleTimeout))
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
	_ = connection.SetWriteDeadline(time.Now().Add(privatePongTimeout))
	if err := websocket.Message.Send(connection, "ping"); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	_ = connection.SetReadDeadline(time.Now().Add(privatePongTimeout))
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
			return nil, exchange.Rejected("decode private subscription frame", err)
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
			return nil, exchange.Rejected(
				"unexpected or duplicate private channel acknowledgement",
				nil,
			)
		}
		delete(pending, payload.Arg.Channel)
	}
	return buffered, nil
}

// eventOnlyPush 让 account、positions 频道只在有变化时推送。不设时 OKX 还会定时推送（positions 约每 5 秒），
// 每次推送都会关闭准入并触发同步。OKX 要求它是 JSON 字符串。
const eventOnlyPush = `{"updateInterval": "0"}`

func (a *Adapter) privateChannels() []map[string]string {
	channels := []map[string]string{
		{"channel": "orders", "instType": a.instrumentType()},
		{"channel": "account", "extraParams": eventOnlyPush},
	}
	if a.config.Market() == exchange.MarketTypeSwap {
		channels = append(channels, map[string]string{
			"channel": "positions", "instType": "SWAP", "extraParams": eventOnlyPush,
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
		return exchange.Rejected("decode private event", err)
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
			TradeID    string `json:"tradeId"`
			FillSz     string `json:"fillSz"`
			FillPx     string `json:"fillPx"`
			FillFee    string `json:"fillFee"`
			FillFeeCcy string `json:"fillFeeCcy"`
			FillPnl    string `json:"fillPnl"`
			ExecType   string `json:"execType"`
			FillTime   string `json:"fillTime"`
		}
		if err := json.Unmarshal(message.Data, &rows); err != nil {
			return exchange.Rejected("decode private orders", err)
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
				Fee: row.FillFee, FeeCcy: row.FillFeeCcy,
				FillPnl: row.FillPnl, ExecType: row.ExecType, FillTime: row.FillTime,
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
			return exchange.Rejected("decode private positions", err)
		}
		for _, row := range rows {
			if row.PosSide != "" && row.PosSide != "net" {
				return exchange.Rejected("hedge position mode is unsupported", nil)
			}
			contracts, err := exchange.DecimalOrZero(row.Pos)
			if err != nil {
				return err
			}
			if row.MgnMode != "cross" {
				// 逐仓持仓只标记、不解析其余字段（也不换算张数）：没有仓位的逐仓行不算。
				if contracts.IsZero() {
					continue
				}
				isolated := exchange.Position{
					Market: a.config.Market(), TradingAccountID: a.config.TradingAccountID,
					ExchangeSymbol: row.InstID, PositionSide: exchange.PositionSideNet,
					SignedQuantity: contracts, MarginMode: exchange.MarginModeIsolated,
					ExchangeUpdatedAt: millisString(row.UTime),
				}
				if err := handler.OnPosition(ctx, isolated); err != nil {
					return err
				}
				continue
			}
			quantity, err := a.toBaseQuantity(row.InstID, contracts)
			if err != nil {
				return err
			}
			position := exchange.Position{
				Market:           a.config.Market(),
				TradingAccountID: a.config.TradingAccountID,
				ExchangeSymbol:   row.InstID, PositionSide: exchange.PositionSideNet,
				SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
				ExchangeUpdatedAt: millisString(row.UTime),
			}
			position.EntryPrice, err = exchange.DecimalOrZero(row.AvgPx)
			if err == nil {
				position.MarkPrice, err = exchange.DecimalOrZero(row.MarkPx)
			}
			if err == nil {
				position.Leverage, err = exchange.DecimalOrZero(row.Lever)
			}
			if err == nil {
				usedMargin := row.IMR
				if usedMargin == "" {
					usedMargin = row.Margin
				}
				position.UsedMargin, err = exchange.DecimalOrZero(usedMargin)
			}
			if err == nil {
				position.LiquidationPrice, err = exchange.DecimalOrZero(row.LiqPx)
			}
			if err == nil {
				position.UnrealizedPnL, err = exchange.DecimalOrZero(row.Upl)
			}
			if err == nil {
				position.RealizedPnL, err = exchange.DecimalOrZero(row.Realized)
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
			return exchange.Rejected("decode private account", err)
		}
		snapshot, err := a.balanceSnapshot(rows[0])
		if err != nil {
			return err
		}
		snapshot.RequiresSync = !snapshot.Present.AvailableFunds
		if err := handler.OnAccountSnapshot(ctx, snapshot); err != nil {
			return err
		}
	}
	return nil
}

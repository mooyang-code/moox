package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

var errListenKeyExpired = errors.New("binance: private stream listen key expired")

type listenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

type spotSubscriptionRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// privateHandshakeTimeout 是私有流拨号、登录与订阅确认阶段的统一期限。
var privateHandshakeTimeout = 15 * time.Second

func (a *Adapter) Subscribe(ctx context.Context, handler execution.AccountEventHandler) error {
	if handler == nil {
		return typedRejected("private event handler is required", nil)
	}
	if a.config.Market() == exchange.MarketTypeSpot {
		return a.subscribeSpotPrivate(ctx, handler)
	}
	return a.subscribeSwapPrivate(ctx, handler)
}

func (a *Adapter) subscribeSpotPrivate(
	ctx context.Context,
	handler execution.AccountEventHandler,
) error {
	connection, _, err := websocket.DefaultDialer.DialContext(
		ctx,
		privateStreamEndpoint(a.config),
		http.Header{"Origin": []string{"https://moox.local"}},
	)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()

	if err := a.spotSubscribeHandshake(connection); err != nil {
		return err
	}
	_ = connection.SetWriteDeadline(time.Time{})
	handler.OnSubscribed()
	return a.receivePrivate(ctx, connection, handler)
}

// spotSubscribeHandshake 发送签名订阅请求并等待 ACK，整个过程受 privateHandshakeTimeout 约束。
func (a *Adapter) spotSubscribeHandshake(connection *websocket.Conn) error {
	// 订阅 ACK 阶段设置统一期限：服务端保持连接但不应答时不能一直阻塞，需要返回错误进入重连
	handshakeDeadline := time.Now().Add(privateHandshakeTimeout)
	_ = connection.SetWriteDeadline(handshakeDeadline)
	_ = connection.SetReadDeadline(handshakeDeadline)
	request := a.newSpotSubscriptionRequest(time.Now().UnixMilli())
	if err := connection.WriteJSON(request); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	var acknowledgement struct {
		Status int `json:"status"`
		Result struct {
			SubscriptionID *int `json:"subscriptionId"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := connection.ReadJSON(&acknowledgement); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	if acknowledgement.Status != http.StatusOK ||
		acknowledgement.Result.SubscriptionID == nil {
		return classifySpotSubscriptionError(
			acknowledgement.Status,
			acknowledgement.Error,
		)
	}
	return nil
}

func classifySpotSubscriptionError(status int, raw json.RawMessage) error {
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &payload)
	cause := fmt.Errorf("status %d, code %d: %s", status, payload.Code, payload.Msg)
	switch {
	case status == http.StatusUnauthorized || payload.Code == -2015:
		return &exchange.Error{
			Kind: exchange.ErrorAuthentication, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	case status == http.StatusTooManyRequests ||
		status == http.StatusTeapot ||
		payload.Code == -1003:
		return &exchange.Error{
			Kind: exchange.ErrorRateLimited, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	case status >= http.StatusInternalServerError:
		return &exchange.Error{
			Kind: exchange.ErrorTransportUnknown, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	default:
		return &exchange.Error{
			Kind: exchange.ErrorRejected, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	}
}

func (a *Adapter) newSpotSubscriptionRequest(timestamp int64) spotSubscriptionRequest {
	params := url.Values{
		"apiKey":     []string{a.credential.APIKey},
		"recvWindow": []string{fmt.Sprint(recvWindow)},
		"timestamp":  []string{fmt.Sprint(timestamp)},
	}
	params.Set("signature", fmt.Sprintf(
		"%x",
		hmacSha256([]byte(a.credential.APISecret), []byte(params.Encode())),
	))
	return spotSubscriptionRequest{
		ID: "private-" + fmt.Sprint(timestamp), Method: "userDataStream.subscribe.signature",
		Params: map[string]any{
			"apiKey": a.credential.APIKey, "recvWindow": recvWindow,
			"timestamp": timestamp, "signature": params.Get("signature"),
		},
	}
}

func (a *Adapter) subscribeSwapPrivate(
	ctx context.Context,
	handler execution.AccountEventHandler,
) error {
	path := "/fapi/v1/listenKey"
	raw, err := a.client().Do(ctx, &httpclient.Request{
		Method: http.MethodPost,
		Path:   path,
		Headers: map[string]string{
			"X-MBX-APIKEY": a.credential.APIKey,
		},
	})
	if err != nil {
		return fmt.Errorf("binance: create private stream: %w", err)
	}
	var key listenKeyResponse
	if err := json.Unmarshal(raw, &key); err != nil || key.ListenKey == "" {
		return typedRejected("invalid private stream listen key", err)
	}
	endpoint := privateStreamEndpoint(a.config) + key.ListenKey
	connection, _, err := websocket.DefaultDialer.DialContext(
		ctx,
		endpoint,
		http.Header{"Origin": []string{"https://moox.local"}},
	)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()

	keepaliveDone := make(chan struct{})
	defer close(keepaliveDone)
	go a.keepListenKeyAlive(ctx, path, key.ListenKey, connection, keepaliveDone)

	handler.OnSubscribed()
	return a.receivePrivate(ctx, connection, handler)
}

func (a *Adapter) receivePrivate(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
) error {
	return a.receivePrivateWithHeartbeat(
		ctx,
		connection,
		handler,
		a.privateHeartbeatTimeout(),
	)
}

func (a *Adapter) privateHeartbeatTimeout() time.Duration {
	if a.config.Market() == exchange.MarketTypeSwap {
		// USD-M sends a server ping roughly every three minutes.
		return 4 * time.Minute
	}
	return 60 * time.Second
}

func (a *Adapter) receivePrivateWithHeartbeat(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
	heartbeatTimeout time.Duration,
) error {
	return receiveStream(ctx, connection, handler, heartbeatTimeout, a.dispatchPrivate)
}

// receiveStream 读取私有流并逐条分派；心跳超时、读失败或分派出错都结束并返回，由会话重连。
// 普通账户（按市场）和统一账户（整个账户一条流）共用。
func receiveStream(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
	heartbeatTimeout time.Duration,
	dispatch func(context.Context, []byte, execution.AccountEventHandler) error,
) error {
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()
	refreshDeadline := func() error {
		return connection.SetReadDeadline(time.Now().Add(heartbeatTimeout))
	}
	if err := refreshDeadline(); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	connection.SetPingHandler(func(data string) error {
		if err := refreshDeadline(); err != nil {
			return err
		}
		return connection.WriteControl(
			websocket.PongMessage,
			[]byte(data),
			time.Now().Add(5*time.Second),
		)
	})
	for {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
		}
		if err := refreshDeadline(); err != nil {
			return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
		}
		if err := dispatch(ctx, payload, handler); err != nil {
			return err
		}
	}
}

func (a *Adapter) keepListenKeyAlive(
	ctx context.Context,
	path string,
	listenKey string,
	connection *websocket.Conn,
	done <-chan struct{},
) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			// 限流暂停期间跳过这次续期，1 分钟后再试：listenKey 有效期 60 分钟，等满 30 分钟会过期断流。
			if !a.client().Status().PausedUntil.IsZero() {
				ticker.Reset(time.Minute)
				continue
			}
			_, err := a.client().Do(ctx, &httpclient.Request{
				Method: http.MethodPut,
				Path:   path,
				Query:  url.Values{"listenKey": []string{listenKey}},
				Headers: map[string]string{
					"X-MBX-APIKEY": a.credential.APIKey,
				},
			})
			// 限流（确定未受理）或结果未知的续期 1 分钟后重试，只有明确失败才断开重连。
			if exchange.NotAccepted(err) || exchange.IsKind(err, exchange.ErrorTransportUnknown) {
				ticker.Reset(time.Minute)
				continue
			}
			if err != nil {
				_ = connection.Close()
				return
			}
			ticker.Reset(30 * time.Minute)
		}
	}
}

// privateOrder 是私有流订单事件里按精确键取出的字段，现货 executionReport 和合约 ORDER_TRADE_UPDATE 共用。
type privateOrder struct {
	payload       orderPayload
	execution     string
	tradeID       string
	lastQty       string
	lastPrice     string
	fee           string
	feeAsset      string
	realizedPnL   string
	maker         bool
	transactionAt int64
}

func (a *Adapter) dispatchPrivate(
	ctx context.Context,
	payload []byte,
	handler execution.AccountEventHandler,
) error {
	root, err := exactFields(payload)
	if err != nil {
		return typedRejected("decode private event", err)
	}
	// WebSocket API 订阅把事件包在 event 里，listenKey 流直接是事件本身。
	if _, wrapped := root.value("event"); wrapped {
		if root, err = root.object("event"); err != nil {
			return typedRejected("decode private event wrapper", err)
		}
	}
	// 没有 e 的是 WS API 应答之类的非事件帧，按未知事件忽略。
	event, err := root.str("e")
	if err != nil {
		return typedRejected("decode private event", err)
	}
	// 先按事件类型分派：o、a 在不同事件里类型不同（executionReport 的 o 是订单类型字符串）。
	var order privateOrder
	switch event {
	case "listenKeyExpired", "eventStreamTerminated":
		// 两者之后都不会再有事件，必须重连订阅；不能当成未知事件忽略。
		return errListenKeyExpired
	case "ACCOUNT_UPDATE":
		return a.dispatchFuturesAccount(ctx, root, handler)
	case "outboundAccountPosition":
		return a.dispatchSpotAccount(ctx, root, handler)
	case "ORDER_TRADE_UPDATE":
		body, objectErr := root.object("o")
		if objectErr != nil {
			return typedRejected("decode futures private order", objectErr)
		}
		order, err = futuresPrivateOrder(body)
		if err != nil {
			return typedRejected("decode futures private order", err)
		}
	case "executionReport":
		order, err = spotPrivateOrder(root)
		if err != nil {
			return typedRejected("decode spot private order", err)
		}
	default:
		return nil
	}
	current, err := a.orderFromPayload(order.payload)
	if err != nil {
		return err
	}
	if err := handler.OnOrder(ctx, current); err != nil {
		return err
	}
	// 现货没有成交时 t 是 -1。
	if order.execution != "TRADE" || order.tradeID == "" || order.tradeID == "-1" {
		return nil
	}
	fill, err := fillFromPrivateOrder(a.config.Market(), a.config.SettlementAsset, order)
	if err != nil {
		return err
	}
	return handler.OnFill(ctx, fill)
}

// fillFromPrivateOrder 把私有流订单事件里的成交部分转成成交记录；普通账户和统一账户共用。
func fillFromPrivateOrder(market exchange.MarketType, settlementAsset string, order privateOrder) (exchange.Fill, error) {
	quantity, err := decimalOrZero(order.lastQty)
	if err != nil {
		return exchange.Fill{}, err
	}
	price, err := decimalOrZero(order.lastPrice)
	if err != nil {
		return exchange.Fill{}, err
	}
	fee, err := decimalOrZero(order.fee)
	if err != nil {
		return exchange.Fill{}, err
	}
	pnl, err := decimalOrZero(order.realizedPnL)
	if err != nil {
		return exchange.Fill{}, err
	}
	positionSide := exchange.PositionSideUnspecified
	if market == exchange.MarketTypeSwap {
		if order.payload.PositionSide != "" && order.payload.PositionSide != "BOTH" {
			return exchange.Fill{}, typedRejected("hedge fill is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	return exchange.Fill{
		Market:          market,
		ExchangeTradeID: order.tradeID,
		ExchangeOrderID: order.payload.OrderID.String(),
		ClientOrderID:   order.payload.ClientOrderID,
		ExchangeSymbol:  order.payload.Symbol,
		Side:            exchange.Side(order.payload.Side),
		PositionSide:    positionSide,
		Quantity:        quantity,
		Price:           price,
		Fee:             fee.Abs(),
		FeeAsset:        order.feeAsset,
		RealizedPnL:     pnl,
		SettlementAsset: settlementAsset,
		LiquidityRole:   binanceLiquidityRole(order.maker),
		TradedAt:        millis(order.transactionAt),
	}, nil
}

// spotPrivateOrder 解析现货 executionReport。撤单事件的 c 是撤单请求自己的单号，原单号在 C；
// 其他事件（含改单 REPLACED）一律用 c。
func spotPrivateOrder(event fields) (privateOrder, error) {
	for _, key := range []string{"s", "i", "x", "X"} {
		if err := event.required(key); err != nil {
			return privateOrder{}, err
		}
	}
	var order privateOrder
	var err error
	var orderID, clientID, original string
	values := []struct {
		key    string
		target *string
	}{
		{"s", &order.payload.Symbol}, {"i", &orderID}, {"c", &clientID}, {"C", &original},
		{"S", &order.payload.Side}, {"o", &order.payload.Type}, {"f", &order.payload.TimeInForce},
		{"q", &order.payload.OrigQty}, {"p", &order.payload.Price}, {"X", &order.payload.Status},
		{"z", &order.payload.ExecutedQty}, {"Z", &order.payload.CumulativeQuoteQty},
		{"x", &order.execution}, {"t", &order.tradeID}, {"l", &order.lastQty},
		{"L", &order.lastPrice}, {"n", &order.fee}, {"N", &order.feeAsset},
	}
	for _, value := range values {
		if *value.target, err = event.str(value.key); err != nil {
			return privateOrder{}, err
		}
	}
	if order.maker, err = event.bool("m"); err != nil {
		return privateOrder{}, err
	}
	if order.transactionAt, err = event.int64("T"); err != nil {
		return privateOrder{}, err
	}
	if order.payload.Time, err = event.int64("O"); err != nil {
		return privateOrder{}, err
	}
	if order.execution == "CANCELED" && original != "" {
		clientID = original
	}
	order.payload.OrderID = json.Number(orderID)
	order.payload.ClientOrderID = clientID
	order.payload.UpdateTime = order.transactionAt
	return order, nil
}

// futuresPrivateOrder 解析合约 ORDER_TRADE_UPDATE 的 o 对象。
func futuresPrivateOrder(body fields) (privateOrder, error) {
	for _, key := range []string{"s", "i", "x", "X"} {
		if err := body.required(key); err != nil {
			return privateOrder{}, err
		}
	}
	var order privateOrder
	var err error
	var orderID string
	values := []struct {
		key    string
		target *string
	}{
		{"s", &order.payload.Symbol}, {"i", &orderID}, {"c", &order.payload.ClientOrderID},
		{"S", &order.payload.Side}, {"o", &order.payload.Type}, {"f", &order.payload.TimeInForce},
		{"ps", &order.payload.PositionSide}, {"q", &order.payload.OrigQty}, {"p", &order.payload.Price},
		{"z", &order.payload.ExecutedQty}, {"ap", &order.payload.AvgPrice}, {"X", &order.payload.Status},
		{"x", &order.execution}, {"t", &order.tradeID}, {"l", &order.lastQty},
		{"L", &order.lastPrice}, {"n", &order.fee}, {"N", &order.feeAsset}, {"rp", &order.realizedPnL},
	}
	for _, value := range values {
		if *value.target, err = body.str(value.key); err != nil {
			return privateOrder{}, err
		}
	}
	if order.payload.ReduceOnly, err = body.bool("R"); err != nil {
		return privateOrder{}, err
	}
	if order.maker, err = body.bool("m"); err != nil {
		return privateOrder{}, err
	}
	if order.transactionAt, err = body.int64("T"); err != nil {
		return privateOrder{}, err
	}
	order.payload.OrderID = json.Number(orderID)
	order.payload.UpdateTime = order.transactionAt
	return order, nil
}

func binanceLiquidityRole(maker bool) string {
	if maker {
		return "MAKER"
	}
	return "TAKER"
}

func (a *Adapter) dispatchFuturesAccount(
	ctx context.Context,
	root fields,
	handler execution.AccountEventHandler,
) error {
	// T 是事务（余额实际变更）时间；E 只是消息生成时间，可能晚于 T，不能与 REST 的余额更新时间比较。
	eventTime, err := root.int64("T")
	if err == nil && eventTime <= 0 {
		eventTime, err = root.int64("E")
	}
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	body, err := root.object("a")
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	balances, err := body.objects("B")
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	positions, err := body.objects("P")
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: millis(eventTime),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
		RequiresSync:      true,
	}
	for _, row := range balances {
		var asset, wallet, cross string
		for key, target := range map[string]*string{"a": &asset, "wb": &wallet, "cw": &cross} {
			if *target, err = row.str(key); err != nil {
				return typedRejected("decode futures account event", err)
			}
		}
		total, err := decimalOrZero(wallet)
		if err != nil {
			return err
		}
		available, err := decimalOrZero(cross)
		if err != nil {
			return err
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Wallet: a.wallet(),
			Asset:  asset, Available: available, Locked: total.Sub(available), Total: total,
		})
	}
	if err := handler.OnAccountSnapshot(ctx, snapshot); err != nil {
		return err
	}
	for _, row := range positions {
		var symbol, amount, entry, unrealized, marginType, side string
		for key, target := range map[string]*string{
			"s": &symbol, "pa": &amount, "ep": &entry, "up": &unrealized, "mt": &marginType, "ps": &side,
		} {
			if *target, err = row.str(key); err != nil {
				return typedRejected("decode futures account event", err)
			}
		}
		if side != "" && side != "BOTH" {
			return typedRejected("hedge position mode is unsupported", nil)
		}
		if !strings.EqualFold(marginType, "cross") {
			return typedRejected("isolated margin is unsupported", nil)
		}
		quantity, err := decimalOrZero(amount)
		if err != nil {
			return err
		}
		position := exchange.Position{
			Market:           a.config.Market(),
			TradingAccountID: a.config.TradingAccountID,
			ExchangeSymbol:   symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: millis(eventTime),
		}
		position.EntryPrice, err = decimalOrZero(entry)
		if err == nil {
			position.UnrealizedPnL, err = decimalOrZero(unrealized)
		}
		if err != nil {
			return err
		}
		if err := handler.OnPosition(ctx, position); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) dispatchSpotAccount(
	ctx context.Context,
	root fields,
	handler execution.AccountEventHandler,
) error {
	updatedAt, err := root.int64("u")
	if err != nil {
		return typedRejected("decode spot account event", err)
	}
	balances, err := root.objects("B")
	if err != nil {
		return typedRejected("decode spot account event", err)
	}
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: millis(updatedAt),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
	}
	for _, row := range balances {
		var asset, free, locked string
		for key, target := range map[string]*string{"a": &asset, "f": &free, "l": &locked} {
			if *target, err = row.str(key); err != nil {
				return typedRejected("decode spot account event", err)
			}
		}
		available, err := decimalOrZero(free)
		if err != nil {
			return err
		}
		lockedValue, err := decimalOrZero(locked)
		if err != nil {
			return err
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Wallet: a.wallet(),
			Asset:  asset, Available: available, Locked: lockedValue,
			Total: available.Add(lockedValue),
		})
	}
	return handler.OnAccountSnapshot(ctx, snapshot)
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

type InstrumentRecord struct {
	Exchange             string
	Environment          string
	MarketType           string
	ExchangeSymbol       string
	InstrumentID         string
	BaseAsset            string
	QuoteAsset           string
	SettlementAsset      string
	Linear               bool
	ContractValue        string
	ContractValueAsset   string
	ExchangeQuantityStep string
	MinExchangeQuantity  string
	PriceTick            string
	MinNotional          string
	// MaxBuyPriceRatio、MinSellPriceRatio 是交易所的价格偏离限制（见 exchange.Instrument），"0" 表示没有。
	MaxBuyPriceRatio  string
	MinSellPriceRatio string
	Status            string
	ExchangeUpdatedAt int64
}

func (tx *Tx) UpsertInstrument(record InstrumentRecord) error {
	tx.instrumentsWritten = true
	if tx.store != nil {
		tx.store.instrumentSets.Invalidate()
	}
	if record.InstrumentID == "" {
		record.InstrumentID = record.ExchangeSymbol
	}
	if record.Environment == "" {
		record.Environment = "PRODUCTION"
	}
	if record.Exchange == "" || record.Environment == "" || record.MarketType == "" ||
		record.ExchangeSymbol == "" || record.InstrumentID == "" ||
		record.BaseAsset == "" || record.QuoteAsset == "" || record.PriceTick == "" ||
		record.ExchangeQuantityStep == "" || record.Status == "" {
		return fmt.Errorf("%w: incomplete instrument", ErrInvalidRecord)
	}
	var err error
	record.PriceTick, err = canonicalDecimal(record.PriceTick, "price tick", decimalPositive)
	if err != nil {
		return err
	}
	record.ExchangeQuantityStep, err = canonicalDecimal(
		record.ExchangeQuantityStep,
		"Exchange quantity step",
		decimalPositive,
	)
	if err != nil {
		return err
	}
	if exchange.RuleFor(record.MarketType).ContractValue &&
		(!record.Linear || record.ContractValueAsset != record.BaseAsset) {
		return fmt.Errorf("%w: incomplete SWAP instrument", ErrInvalidRecord)
	}
	if exchange.RuleFor(record.MarketType).ContractValue {
		record.ContractValue, err = canonicalDecimal(
			record.ContractValue,
			"contract value",
			decimalPositive,
		)
		if err != nil {
			return err
		}
		record.MinExchangeQuantity, err = canonicalDecimal(
			record.MinExchangeQuantity,
			"minimum Exchange quantity",
			decimalPositive,
		)
		if err != nil {
			return err
		}
	}
	record.ContractValue, err = canonicalDefaultZero(
		record.ContractValue,
		"contract value",
		decimalNonNegative,
	)
	if err != nil {
		return err
	}
	record.MinExchangeQuantity, err = canonicalDefaultZero(
		record.MinExchangeQuantity,
		"minimum Exchange quantity",
		decimalNonNegative,
	)
	if err != nil {
		return err
	}
	record.MinNotional, err = canonicalDefaultZero(
		record.MinNotional,
		"minimum notional",
		decimalNonNegative,
	)
	if err != nil {
		return err
	}
	if record.MaxBuyPriceRatio, err = canonicalDefaultZero(record.MaxBuyPriceRatio, "max buy price ratio", decimalNonNegative); err != nil {
		return err
	}
	if record.MinSellPriceRatio, err = canonicalDefaultZero(record.MinSellPriceRatio, "min sell price ratio", decimalNonNegative); err != nil {
		return err
	}
	return writeError(tx.db.Exec(execUpsertInstrument,
		record.Exchange, record.Environment, record.MarketType, record.ExchangeSymbol, record.InstrumentID,
		record.BaseAsset, record.QuoteAsset, record.SettlementAsset, record.Linear,
		record.ContractValue, record.ContractValueAsset,
		record.ExchangeQuantityStep, record.MinExchangeQuantity,
		record.PriceTick, record.MinNotional, record.MaxBuyPriceRatio, record.MinSellPriceRatio, record.Status,
		record.ExchangeUpdatedAt,
	).Error)
}

// execUpsertInstrument 写入或更新一个标的。
const execUpsertInstrument = `INSERT INTO t_trade_instruments (
			c_exchange, c_environment, c_market_type, c_exchange_symbol, c_instrument_id, c_base_asset,
			c_quote_asset, c_settlement_asset, c_linear, c_contract_value,
			c_contract_value_asset, c_exchange_quantity_step,
			c_min_exchange_quantity, c_price_tick, c_min_notional,
			c_max_buy_price_ratio, c_min_sell_price_ratio, c_status,
			c_exchange_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_exchange, c_environment, c_market_type, c_exchange_symbol) DO UPDATE SET
			c_instrument_id = excluded.c_instrument_id,
			c_base_asset = excluded.c_base_asset,
			c_quote_asset = excluded.c_quote_asset,
			c_settlement_asset = excluded.c_settlement_asset,
			c_linear = excluded.c_linear,
			c_contract_value = excluded.c_contract_value,
			c_contract_value_asset = excluded.c_contract_value_asset,
			c_exchange_quantity_step = excluded.c_exchange_quantity_step,
			c_min_exchange_quantity = excluded.c_min_exchange_quantity,
			c_price_tick = excluded.c_price_tick,
			c_min_notional = excluded.c_min_notional,
			c_max_buy_price_ratio = excluded.c_max_buy_price_ratio,
			c_min_sell_price_ratio = excluded.c_min_sell_price_ratio,
			c_status = excluded.c_status,
			c_exchange_updated_at = excluded.c_exchange_updated_at,
			c_mtime = CURRENT_TIMESTAMP`

func (s *Store) GetInstrument(
	ctx context.Context,
	exchange string,
	marketType string,
	symbol string,
) (InstrumentRecord, error) {
	return s.GetInstrumentInEnvironment(ctx, exchange, "PRODUCTION", marketType, symbol)
}

func (s *Store) GetInstrumentInEnvironment(ctx context.Context, exchange, environment, marketType, exchangeSymbol string) (InstrumentRecord, error) {
	set, err := s.instrumentSet(ctx, instrumentScope{exchange, environment, marketType})
	if err != nil {
		return InstrumentRecord{}, err
	}
	return set.symbol(exchangeSymbol)
}

func (s *Store) GetInstrumentByID(ctx context.Context, instrumentID string) (InstrumentRecord, error) {
	return getInstrumentByID(s.db.WithContext(ctx), instrumentID)
}

func (s *Store) GetInstrumentByIDForAccount(ctx context.Context, spaceID, tradingAccountID, instrumentID string) (InstrumentRecord, error) {
	var record InstrumentRecord
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		record, err = tx.GetInstrumentByIDForAccount(spaceID, tradingAccountID, instrumentID)
		return err
	})
	return record, err
}

func (s *Store) GetInstrumentByIDScoped(ctx context.Context, instrumentID, exchange, marketType string) (InstrumentRecord, error) {
	return getInstrumentByID(s.db.WithContext(ctx), instrumentID, exchange, "", marketType)
}

func (s *Store) ListInstruments(
	ctx context.Context,
	exchange string,
	marketType string,
) ([]InstrumentRecord, error) {
	return s.ListInstrumentsInEnvironment(ctx, exchange, "PRODUCTION", marketType)
}

func (s *Store) ListInstrumentsInEnvironment(
	ctx context.Context,
	exchange string,
	environment string,
	marketType string,
) ([]InstrumentRecord, error) {
	set, err := s.instrumentSet(ctx, instrumentScope{exchange, environment, marketType})
	if err != nil {
		return nil, err
	}
	return set.records(), nil
}

// queryInstruments 从库里读一个场所的全部标的，只由标的缓存调用。
func queryInstruments(db *gorm.DB, exchange, environment, marketType string) ([]InstrumentRecord, error) {
	var rows []struct {
		Exchange             string `gorm:"column:c_exchange"`
		Environment          string `gorm:"column:c_environment"`
		MarketType           string `gorm:"column:c_market_type"`
		ExchangeSymbol       string `gorm:"column:c_exchange_symbol"`
		InstrumentID         string `gorm:"column:c_instrument_id"`
		BaseAsset            string `gorm:"column:c_base_asset"`
		QuoteAsset           string `gorm:"column:c_quote_asset"`
		SettlementAsset      string `gorm:"column:c_settlement_asset"`
		Linear               bool   `gorm:"column:c_linear"`
		ContractValue        string `gorm:"column:c_contract_value"`
		ContractValueAsset   string `gorm:"column:c_contract_value_asset"`
		ExchangeQuantityStep string `gorm:"column:c_exchange_quantity_step"`
		MinExchangeQuantity  string `gorm:"column:c_min_exchange_quantity"`
		PriceTick            string `gorm:"column:c_price_tick"`
		MinNotional          string `gorm:"column:c_min_notional"`
		MaxBuyPriceRatio     string `gorm:"column:c_max_buy_price_ratio"`
		MinSellPriceRatio    string `gorm:"column:c_min_sell_price_ratio"`
		Status               string `gorm:"column:c_status"`
		ExchangeUpdatedAt    int64  `gorm:"column:c_exchange_updated_at"`
	}
	if err := db.Table("t_trade_instruments").
		Where("c_exchange = ? AND c_environment = ? AND c_market_type = ?", exchange, environment, marketType).
		Order("c_exchange_symbol").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]InstrumentRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, InstrumentRecord{
			Exchange: row.Exchange, Environment: row.Environment, MarketType: row.MarketType, ExchangeSymbol: row.ExchangeSymbol,
			InstrumentID: row.InstrumentID, BaseAsset: row.BaseAsset,
			QuoteAsset: row.QuoteAsset, SettlementAsset: row.SettlementAsset,
			Linear: row.Linear, ContractValue: row.ContractValue,
			ContractValueAsset:   row.ContractValueAsset,
			ExchangeQuantityStep: row.ExchangeQuantityStep,
			MinExchangeQuantity:  row.MinExchangeQuantity,
			PriceTick:            row.PriceTick, MinNotional: row.MinNotional,
			MaxBuyPriceRatio: row.MaxBuyPriceRatio, MinSellPriceRatio: row.MinSellPriceRatio,
			Status: row.Status, ExchangeUpdatedAt: row.ExchangeUpdatedAt,
		})
	}
	return records, nil
}

func (s *Store) ListInstrumentsForAccount(ctx context.Context, tradingAccountID string) ([]InstrumentRecord, error) {
	account, err := s.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return nil, err
	}
	environment := account.Environment
	if account.ExecutionMode == "PAPER" || environment == "" {
		environment = "PRODUCTION"
	}
	var result []InstrumentRecord
	for _, market := range account.Markets {
		items, err := s.ListInstrumentsInEnvironment(ctx, account.Exchange, environment, market)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 && environment != "PRODUCTION" {
			if items, err = s.ListInstrumentsInEnvironment(ctx, account.Exchange, "PRODUCTION", market); err != nil {
				return nil, err
			}
		}
		result = append(result, items...)
	}
	return result, nil
}

func (tx *Tx) GetInstrument(
	exchange string,
	marketType string,
	symbol string,
) (InstrumentRecord, error) {
	return tx.GetInstrumentInEnvironment(exchange, "PRODUCTION", marketType, symbol)
}

func (tx *Tx) GetInstrumentInEnvironment(exchange, environment, marketType, exchangeSymbol string) (InstrumentRecord, error) {
	set, err := tx.instrumentSet(instrumentScope{exchange, environment, marketType})
	if err != nil {
		return InstrumentRecord{}, err
	}
	return set.symbol(exchangeSymbol)
}

func (tx *Tx) GetInstrumentByID(instrumentID string) (InstrumentRecord, error) {
	return getInstrumentByID(tx.db, instrumentID)
}

func (tx *Tx) GetInstrumentByIDForAccount(spaceID, tradingAccountID, instrumentID string) (InstrumentRecord, error) {
	identity, err := tx.accountIdentity(spaceID, tradingAccountID)
	if err != nil {
		return InstrumentRecord{}, err
	}
	for _, market := range identity.Markets {
		set, err := tx.instrumentSet(instrumentScope{identity.Exchange, identity.Environment, market})
		if err != nil {
			return InstrumentRecord{}, err
		}
		if record, lookupErr := set.id(instrumentID); lookupErr == nil {
			return record, nil
		}
	}
	return InstrumentRecord{}, gorm.ErrRecordNotFound
}

func getInstrumentByID(db *gorm.DB, instrumentID string, scope ...string) (InstrumentRecord, error) {
	if strings.TrimSpace(instrumentID) == "" {
		return InstrumentRecord{}, fmt.Errorf("%w: empty instrument ID", ErrInvalidRecord)
	}
	var row struct {
		Exchange             string `gorm:"column:c_exchange"`
		Environment          string `gorm:"column:c_environment"`
		MarketType           string `gorm:"column:c_market_type"`
		ExchangeSymbol       string `gorm:"column:c_exchange_symbol"`
		InstrumentID         string `gorm:"column:c_instrument_id"`
		BaseAsset            string `gorm:"column:c_base_asset"`
		QuoteAsset           string `gorm:"column:c_quote_asset"`
		SettlementAsset      string `gorm:"column:c_settlement_asset"`
		Linear               bool   `gorm:"column:c_linear"`
		ContractValue        string `gorm:"column:c_contract_value"`
		ContractValueAsset   string `gorm:"column:c_contract_value_asset"`
		ExchangeQuantityStep string `gorm:"column:c_exchange_quantity_step"`
		MinExchangeQuantity  string `gorm:"column:c_min_exchange_quantity"`
		PriceTick            string `gorm:"column:c_price_tick"`
		MinNotional          string `gorm:"column:c_min_notional"`
		MaxBuyPriceRatio     string `gorm:"column:c_max_buy_price_ratio"`
		MinSellPriceRatio    string `gorm:"column:c_min_sell_price_ratio"`
		Status               string `gorm:"column:c_status"`
		ExchangeUpdatedAt    int64  `gorm:"column:c_exchange_updated_at"`
	}
	query := db.Table("t_trade_instruments").Where("c_instrument_id = ?", instrumentID)
	if len(scope) >= 1 && scope[0] != "" {
		query = query.Where("c_exchange = ?", scope[0])
	}
	if len(scope) >= 2 && scope[1] != "" {
		query = query.Where("c_environment = ?", scope[1])
	}
	if len(scope) >= 3 && scope[2] != "" {
		query = query.Where("c_market_type = ?", scope[2])
	}
	err := query.Take(&row).Error
	return InstrumentRecord{
		Exchange: row.Exchange, Environment: row.Environment, MarketType: row.MarketType,
		ExchangeSymbol: row.ExchangeSymbol, InstrumentID: row.InstrumentID,
		BaseAsset: row.BaseAsset, QuoteAsset: row.QuoteAsset, SettlementAsset: row.SettlementAsset,
		Linear: row.Linear, ContractValue: row.ContractValue, ContractValueAsset: row.ContractValueAsset,
		ExchangeQuantityStep: row.ExchangeQuantityStep, MinExchangeQuantity: row.MinExchangeQuantity,
		PriceTick: row.PriceTick, MinNotional: row.MinNotional, Status: row.Status,
		MaxBuyPriceRatio: row.MaxBuyPriceRatio, MinSellPriceRatio: row.MinSellPriceRatio,
		ExchangeUpdatedAt: row.ExchangeUpdatedAt,
	}, err
}

type OrderRecord struct {
	SpaceID          string
	OrderID          string
	TradingAccountID string
	ClientOrderID    string
	ExchangeOrderID  string
	Exchange         string
	MarketType       string
	InstrumentID     string
	ExchangeSymbol   string
	OrderType        string
	TimeInForce      string
	Side             string
	PositionSide     string
	Quantity         string
	LimitPrice       *string
	ReferencePrice   string
	ReferencePriceAt int64
	ReduceOnly       bool
	OwnerType        string
	OwnerID          string
	LogicalAccountID string
	RunnerID         string
	PositionBefore   string
	State            string
	// Attempt 是伪市价单的第几次尝试，从 0 开始。
	Attempt                   int
	FilledQuantity            string
	AveragePrice              string
	ReservedAsset             string
	ReservedQuantity          string
	RemainingReservedQuantity string
	PaperExecutionPrice       *string
	FirstMatchPending         bool
	RejectReason              string
	ExchangeUpdatedAt         int64
	Version                   uint64
	SubmittedAt               int64
	FinishedAt                int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type orderRow struct {
	SpaceID                   string    `gorm:"column:c_space_id"`
	OrderID                   string    `gorm:"column:c_order_id"`
	TradingAccountID          string    `gorm:"column:c_trading_account_id"`
	ClientOrderID             string    `gorm:"column:c_client_order_id"`
	ExchangeOrderID           string    `gorm:"column:c_exchange_order_id"`
	Exchange                  string    `gorm:"column:c_exchange"`
	MarketType                string    `gorm:"column:c_market_type"`
	InstrumentID              string    `gorm:"column:c_instrument_id"`
	ExchangeSymbol            string    `gorm:"column:c_exchange_symbol"`
	OrderType                 string    `gorm:"column:c_order_type"`
	TimeInForce               string    `gorm:"column:c_time_in_force"`
	Side                      string    `gorm:"column:c_side"`
	PositionSide              string    `gorm:"column:c_position_side"`
	Quantity                  string    `gorm:"column:c_quantity"`
	LimitPrice                *string   `gorm:"column:c_limit_price"`
	ReferencePrice            string    `gorm:"column:c_reference_price"`
	ReferencePriceAt          int64     `gorm:"column:c_reference_price_at"`
	ReduceOnly                bool      `gorm:"column:c_reduce_only"`
	OwnerType                 string    `gorm:"column:c_owner_type"`
	OwnerID                   string    `gorm:"column:c_owner_id"`
	LogicalAccountID          *string   `gorm:"column:c_logical_account_id"`
	RunnerID                  *string   `gorm:"column:c_runner_id"`
	PositionBefore            string    `gorm:"column:c_position_before"`
	State                     string    `gorm:"column:c_state"`
	Attempt                   int       `gorm:"column:c_attempt"`
	FilledQuantity            string    `gorm:"column:c_filled_quantity"`
	AveragePrice              string    `gorm:"column:c_average_price"`
	ReservedAsset             string    `gorm:"column:c_reserved_asset"`
	ReservedQuantity          string    `gorm:"column:c_reserved_quantity"`
	RemainingReservedQuantity string    `gorm:"column:c_remaining_reserved_quantity"`
	PaperExecutionPrice       *string   `gorm:"column:c_paper_execution_price"`
	FirstMatchPending         bool      `gorm:"column:c_first_match_pending"`
	RejectReason              string    `gorm:"column:c_reject_reason"`
	ExchangeUpdatedAt         int64     `gorm:"column:c_exchange_updated_at"`
	Version                   uint64    `gorm:"column:c_version"`
	SubmittedAt               int64     `gorm:"column:c_submitted_at"`
	FinishedAt                int64     `gorm:"column:c_finished_at"`
	CreatedAt                 time.Time `gorm:"column:c_ctime"`
	UpdatedAt                 time.Time `gorm:"column:c_mtime"`
}

func (tx *Tx) CreateOrder(record OrderRecord) error {
	if record.SpaceID == "" || record.OrderID == "" || record.TradingAccountID == "" ||
		record.ClientOrderID == "" || record.ExchangeSymbol == "" ||
		record.OrderType == "" || record.Side == "" ||
		record.Quantity == "" || record.ReferencePrice == "" || record.OwnerType == "" ||
		record.State == "" {
		return fmt.Errorf("%w: incomplete order", ErrInvalidRecord)
	}
	if err := validateOrderOwnership(tx, record); err != nil {
		return err
	}
	identity, err := tx.accountIdentity(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if err := validateOrDeriveIdentity(&record.Exchange, identity.Exchange, "order Exchange"); err != nil {
		return err
	}
	if err := identity.resolveMarket(&record.MarketType, "order"); err != nil {
		return err
	}
	var instrument InstrumentRecord
	if set, err := tx.instrumentSet(instrumentScope{record.Exchange, identity.Environment, record.MarketType}); err != nil {
		return err
	} else if found, lookupErr := set.symbol(record.ExchangeSymbol); lookupErr == nil {
		instrument = found
	}
	if instrument.InstrumentID == "" {
		instrument.InstrumentID = record.InstrumentID
		if instrument.InstrumentID == "" {
			instrument.InstrumentID = record.ExchangeSymbol
		}
	}
	if record.InstrumentID != "" && record.InstrumentID != instrument.InstrumentID {
		return fmt.Errorf("%w: order instrument ID does not match exchange identity", ErrConflict)
	}
	record.InstrumentID = instrument.InstrumentID
	if err := canonicalizeOrder(&record); err != nil {
		return err
	}
	if record.Version == 0 {
		record.Version = 1
	}
	return writeError(tx.db.Exec(execInsertOrder,
		record.SpaceID, record.OrderID, record.TradingAccountID, record.ClientOrderID,
		record.ExchangeOrderID, record.Exchange, record.MarketType, record.InstrumentID, record.ExchangeSymbol,
		record.OrderType, record.TimeInForce, record.Side, record.PositionSide,
		record.Quantity, record.LimitPrice, record.ReferencePrice, record.ReferencePriceAt,
		record.ReduceOnly, record.OwnerType, record.OwnerID,
		nullableString(record.LogicalAccountID), nullableString(record.RunnerID), record.PositionBefore, record.State,
		record.Attempt, record.FilledQuantity, record.AveragePrice,
		record.ReservedAsset, record.ReservedQuantity,
		record.RemainingReservedQuantity, record.PaperExecutionPrice, record.FirstMatchPending,
		record.RejectReason, record.ExchangeUpdatedAt, record.Version, record.SubmittedAt, record.FinishedAt,
	).Error)
}

func validateOrderOwnership(tx *Tx, record OrderRecord) error {
	var runnerID *string
	if record.RunnerID != "" {
		value := record.RunnerID
		runnerID = &value
	}
	owner := orderdomain.OrderOwner{
		Type:             orderdomain.OwnerType(record.OwnerType),
		OwnerID:          record.OwnerID,
		LogicalAccountID: record.LogicalAccountID,
		RunnerID:         runnerID,
	}
	if err := owner.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if record.LogicalAccountID == "" {
		return nil
	}
	account, err := tx.GetLogicalAccount(record.SpaceID, record.LogicalAccountID)
	if err != nil {
		return err
	}
	if owner.Type == orderdomain.OwnerTarget && (account.OwnerInstanceID == "" || account.OwnerInstanceID != record.RunnerID) {
		return fmt.Errorf("%w: TARGET instance does not own logical account", ErrConflict)
	}
	var membership int64
	if err := tx.db.Raw(`
		SELECT COUNT(*)
		FROM t_logical_account_members
		WHERE c_space_id = ? AND c_logical_account_id = ?
			AND c_trading_account_id = ?
	`,
		record.SpaceID, record.LogicalAccountID, record.TradingAccountID,
	).Scan(&membership).Error; err != nil {
		return err
	}
	if membership != 1 {
		return fmt.Errorf("%w: order account is not a logical account member", ErrInvalidRecord)
	}
	return nil
}

func (s *Store) GetOrder(
	ctx context.Context,
	spaceID string,
	orderID string,
) (OrderRecord, error) {
	var row orderRow
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where("c_space_id = ? AND c_order_id = ?", spaceID, orderID).
		Take(&row).Error
	return orderRecordFromRow(row), err
}

// GetOrderByClientID 按客户端订单号查单：同一个账户里现货和合约的订单号可以相同，所以要带市场。
func (s *Store) GetOrderByClientID(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	market exchange.MarketType,
	clientOrderID string,
) (OrderRecord, error) {
	var row orderRow
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(
			"c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_client_order_id = ?",
			spaceID,
			tradingAccountID,
			market,
			clientOrderID,
		).
		Take(&row).Error
	return orderRecordFromRow(row), err
}

func (s *Store) GetOrderByExchangeID(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	key exchange.SymbolKey,
	exchangeOrderID string,
) (OrderRecord, error) {
	var row orderRow
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(
			"c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_exchange_order_id = ?",
			spaceID,
			tradingAccountID,
			key.Market,
			key.Symbol,
			exchangeOrderID,
		).
		Take(&row).Error
	return orderRecordFromRow(row), err
}

func (s *Store) ListOrdersForAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	terminalSince int64,
) ([]OrderRecord, error) {
	base := func() *gorm.DB {
		return s.db.WithContext(ctx).Table("t_trade_orders").
			Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID)
	}
	var rows []orderRow
	if terminalSince <= 0 {
		if err := base().Find(&rows).Error; err != nil {
			return nil, err
		}
		return sortedOrderRecords(rows), nil
	}
	// 未结束的和窗口内结束的分两次查，各走一个索引；写成一条 OR 会扫该账户全部历史订单。
	if err := base().Where("c_state IN ?", activeOrderStates).Find(&rows).Error; err != nil {
		return nil, err
	}
	var finished []orderRow
	if err := base().Where("c_finished_at >= ? AND c_state NOT IN ?", terminalSince, activeOrderStates).
		Find(&finished).Error; err != nil {
		return nil, err
	}
	// 两次读不在同一事务里：中间刚结束的订单会两边都读到，保留终态那份。
	seen := make(map[string]struct{}, len(finished))
	for _, row := range finished {
		seen[row.OrderID] = struct{}{}
	}
	for _, row := range rows {
		if _, done := seen[row.OrderID]; !done {
			finished = append(finished, row)
		}
	}
	return sortedOrderRecords(finished), nil
}

// sortedOrderRecords 按订单号排序。排序放在 Go 里做：SQL 里写 ORDER BY c_order_id 会让 SQLite
// 改走主键索引扫描整个空间的订单，而不是用账户和状态索引。
func sortedOrderRecords(rows []orderRow) []OrderRecord {
	records := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orderRecordFromRow(row))
	}
	sort.Slice(records, func(i, j int) bool { return records[i].OrderID < records[j].OrderID })
	return records
}

// activeOrderStates 是未结束订单的全部状态。查未结束订单一律写成 c_state IN，
// NOT IN 用不上状态索引，会随历史订单数线性变慢。
var activeOrderStates = []string{
	"PENDING", "SUBMITTING", "SUBMIT_UNKNOWN", "OPEN", "PARTIALLY_FILLED", "CANCELING", "CANCEL_UNKNOWN",
}

// activeOrderStatesJSON 是 activeOrderStates 的 JSON 数组，给 json_each(?) 用。
var activeOrderStatesJSON = func() string {
	raw, _ := json.Marshal(activeOrderStates)
	return string(raw)
}()

// ListActiveOrdersForAccount 只读未结束的订单。状态写成 IN 才能用上 idx_trade_orders_account_state。
func (s *Store) ListActiveOrdersForAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
) ([]OrderRecord, error) {
	var rows []orderRow
	if err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where("c_space_id = ? AND c_trading_account_id = ? AND c_state IN ?", spaceID, tradingAccountID, activeOrderStates).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return sortedOrderRecords(rows), nil
}

// RejectedTargetAccounts 返回本目标在该标的上被交易所拒绝过的执行账户及拒单原因。
// c_submitted_at 为 0 的是本地校验失败，持仓或资金刷新后可以恢复，不计入；
// 零成交结束的 EXPIRED 单同样计入，否则市价单被交易所过期后会循环重下；
// 伪市价单（限价 + IOC）零成交过期是正常的，由尝试次数控制重试，不计入。
func (s *Store) RejectedTargetAccounts(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	targetID string,
	instrumentID string,
) (map[string]string, error) {
	var rows []orderRow
	if err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(`c_space_id = ? AND c_logical_account_id = ? AND c_owner_type = 'TARGET' AND c_owner_id = ?
			AND c_instrument_id = ? AND c_submitted_at > 0
			AND (c_state = 'REJECTED' OR (c_state = 'EXPIRED' AND c_filled_quantity = '0'
				AND NOT (c_order_type = 'LIMIT' AND c_time_in_force = 'IOC')))`,
			spaceID, logicalAccountID, targetID, instrumentID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	rejected := make(map[string]string, len(rows))
	for _, row := range rows {
		record := orderRecordFromRow(row)
		reason := record.RejectReason
		if reason == "" {
			reason = "交易所未受理订单"
		}
		rejected[record.TradingAccountID] = reason
	}
	return rejected, nil
}

// RecentFilledOrdersForLane 返回这个账户在这个标的上 since（毫秒）之后结束且有成交的订单，
// 不分来源，最近的在前。用于判断持仓是否反映了已成交的目标子单。
func (s *Store) RecentFilledOrdersForLane(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	key exchange.SymbolKey,
	since int64,
) ([]OrderRecord, error) {
	var rows []orderRow
	if err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(`c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
			AND c_finished_at > ? AND c_filled_quantity <> '0'`,
			spaceID, tradingAccountID, key.Market, key.Symbol, since).
		// 同一毫秒结束的订单按写入顺序：订单号是随机的，不能用来排先后。
		Order("c_finished_at DESC, rowid DESC").
		Limit(50).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orderRecordFromRow(row))
	}
	return records, nil
}

// TargetChildOrder 是同一目标、同一执行账户、同一标的已发出的一笔子单摘要。
type TargetChildOrder struct {
	TradingAccountID string `gorm:"column:c_trading_account_id"`
	Side             string `gorm:"column:c_side"`
	OrderType        string `gorm:"column:c_order_type"`
	TimeInForce      string `gorm:"column:c_time_in_force"`
	State            string `gorm:"column:c_state"`
	Quantity         string `gorm:"column:c_quantity"`
	FilledQuantity   string `gorm:"column:c_filled_quantity"`
	Attempt          int    `gorm:"column:c_attempt"`
	// ExchangeOrderID 为空表示交易所从没确认收到这笔单（例如查无此单退回待提交、随后丢弃）。
	ExchangeOrderID string `gorm:"column:c_exchange_order_id"`
	RejectReason    string `gorm:"column:c_reject_reason"`
}

// sqlTargetChildOrders 取同一目标、同一标的提交过的子单。c_submitted_at 前面的 + 让 SQLite 不拿它挑索引：
// 否则规划器会选 idx_trade_orders_target_hourly（只按组合和提交时间），扫遍组合里所有目标的子单；
// 走 idx_trade_orders_logical_owner_state 只扫这一份目标的子单。
const sqlTargetChildOrders = `
	SELECT c_trading_account_id, c_side, c_order_type, c_time_in_force, c_state, c_quantity, c_filled_quantity, c_attempt,
		c_exchange_order_id, c_reject_reason
	FROM t_trade_orders
	WHERE c_space_id = ? AND c_logical_account_id = ? AND c_owner_type = 'TARGET' AND c_owner_id = ?
		AND c_instrument_id = ? AND +c_submitted_at > 0
	ORDER BY +c_submitted_at, c_order_id`

// TargetChildOrders 返回同一目标、同一标的已发出的子单（提交过的，所有执行账户），按提交先后排序。
// 伪市价单的尝试次数按（目标，标的）统计，反向熔断和拆单随机化的种子按执行账户统计，都从这里算。
func (s *Store) TargetChildOrders(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	targetID string,
	instrumentID string,
) ([]TargetChildOrder, error) {
	var orders []TargetChildOrder
	if err := s.db.WithContext(ctx).Raw(sqlTargetChildOrders, spaceID, logicalAccountID, targetID, instrumentID).
		Scan(&orders).Error; err != nil {
		return nil, err
	}
	return orders, nil
}

// sqlTargetEndedRestingInstruments 取一份目标里真的挂过单（限价 GTC、提交过、交易所确认收到）而且挂单已经结束（被撤、过期、被拒）的标的。
// 从没发出去的单（查无此单退回待提交后丢弃，没有交易所单号）不算挂过。c_submitted_at 前面的 + 让查询走
// idx_trade_orders_logical_owner_state，不走 idx_trade_orders_target_hourly（见 sqlTargetChildOrders）。
const sqlTargetEndedRestingInstruments = `
	SELECT DISTINCT c_instrument_id
	FROM t_trade_orders
	WHERE c_space_id = ? AND c_logical_account_id = ? AND c_owner_type = 'TARGET' AND c_owner_id = ?
		AND c_state IN ('CANCELED', 'PARTIALLY_CANCELED', 'EXPIRED', 'REJECTED')
		AND c_order_type = 'LIMIT' AND c_time_in_force = 'GTC' AND +c_submitted_at > 0 AND c_exchange_order_id <> ''`

// TargetEndedRestingInstruments 返回这份目标里挂单已经结束的标的（内部标的 ID）：执行器不再在这些标的上挂单，改吃单，
// 否则挂单到期撤掉后下一轮又挂上，撤了重挂一直循环。
func (s *Store) TargetEndedRestingInstruments(ctx context.Context, spaceID, logicalAccountID, targetID string) (map[string]struct{}, error) {
	var instrumentIDs []string
	if err := s.db.WithContext(ctx).Raw(sqlTargetEndedRestingInstruments, spaceID, logicalAccountID, targetID).
		Scan(&instrumentIDs).Error; err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(instrumentIDs))
	for _, instrumentID := range instrumentIDs {
		result[instrumentID] = struct{}{}
	}
	return result, nil
}

// SubmittedTargetOrder 是单币每小时上限汇总用的订单摘要。
type SubmittedTargetOrder struct {
	InstrumentID   string `gorm:"column:c_instrument_id"`
	State          string `gorm:"column:c_state"`
	Quantity       string `gorm:"column:c_quantity"`
	FilledQuantity string `gorm:"column:c_filled_quantity"`
	ReferencePrice string `gorm:"column:c_reference_price"`
	AveragePrice   string `gorm:"column:c_average_price"`
}

// ListSubmittedTargetOrders 列出组合在 [from, to)（毫秒）内提交过的目标子单，不含被拒绝的和只在本地丢弃的（没提交过）。
// 单币每小时上限从这里汇总，不另建表。
func (s *Store) ListSubmittedTargetOrders(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	from int64,
	to int64,
) ([]SubmittedTargetOrder, error) {
	var rows []SubmittedTargetOrder
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Select("c_instrument_id, c_state, c_quantity, c_filled_quantity, c_reference_price, c_average_price").
		Where(`c_space_id = ? AND c_logical_account_id = ? AND c_owner_type = 'TARGET'
			AND c_submitted_at >= ? AND c_submitted_at < ? AND c_state <> 'REJECTED'`,
			spaceID, logicalAccountID, from, to).
		Scan(&rows).Error
	return rows, err
}

func (s *Store) ListOrdersForLane(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	key exchange.SymbolKey,
) ([]OrderRecord, error) {
	var rows []orderRow
	if err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(
			"c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?",
			spaceID,
			tradingAccountID,
			key.Market,
			key.Symbol,
		).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return sortedOrderRecords(rows), nil
}

type OrderQuery struct {
	LogicalAccountID string
	TradingAccountID string
	InstrumentID     string
	// Market 与 ExchangeSymbol 一起过滤；只给 ExchangeSymbol 不给 Market 会把同名的现货和合约混在一起。
	Market         string
	ExchangeSymbol string
	State          string
	// OwnerType 按订单来源过滤：TARGET、OPERATOR、EXTERNAL、SYSTEM；为空不过滤。
	OwnerType string
	OnlyOpen  bool
	StartTime int64
	EndTime   int64
	Offset    int
	Limit     int
}

func (s *Store) ListOrders(
	ctx context.Context,
	spaceID string,
	query OrderQuery,
) ([]OrderRecord, int64, error) {
	if query.ExchangeSymbol != "" && query.Market == "" {
		return nil, 0, fmt.Errorf("%w: 按交易所代码查询必须同时给市场", ErrInvalidRecord)
	}
	db := s.db.WithContext(ctx).Table("t_trade_orders").
		Where("c_space_id = ?", spaceID)
	if query.LogicalAccountID != "" {
		db = db.Where("c_logical_account_id = ?", query.LogicalAccountID)
	}
	if query.TradingAccountID != "" {
		db = db.Where("c_trading_account_id = ?", query.TradingAccountID)
	}
	if query.InstrumentID != "" {
		db = db.Where("c_instrument_id = ?", query.InstrumentID)
	}
	if query.Market != "" {
		db = db.Where("c_market_type = ?", query.Market)
	}
	if query.ExchangeSymbol != "" {
		db = db.Where("c_exchange_symbol = ?", query.ExchangeSymbol)
	}
	if query.State != "" {
		db = db.Where("c_state = ?", query.State)
	}
	if query.OwnerType != "" {
		db = db.Where("c_owner_type = ?", query.OwnerType)
	}
	if query.OnlyOpen {
		db = db.Where("c_state IN ?", activeOrderStates)
	}
	if query.StartTime > 0 {
		db = db.Where("c_ctime >= datetime(? / 1000, 'unixepoch')", query.StartTime)
	}
	if query.EndTime > 0 {
		db = db.Where("c_ctime <= datetime(? / 1000, 'unixepoch')", query.EndTime)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []orderRow
	if err := db.Order("c_ctime DESC, c_order_id DESC").
		Offset(query.Offset).Limit(query.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	records := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orderRecordFromRow(row))
	}
	return records, total, nil
}

func (tx *Tx) GetOrder(spaceID string, orderID string) (OrderRecord, error) {
	var row orderRow
	result := tx.db.Raw(`
		SELECT * FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id = ?
	`, spaceID, orderID).Scan(&row)
	if result.Error != nil {
		return OrderRecord{}, result.Error
	}
	if result.RowsAffected != 1 {
		return OrderRecord{}, fmt.Errorf("%w: order not found", ErrInvalidRecord)
	}
	return orderRecordFromRow(row), nil
}

func (tx *Tx) FindOrderForFill(
	spaceID string,
	tradingAccountID string,
	clientOrderID string,
	exchangeOrderID string,
	key exchange.SymbolKey,
) (OrderRecord, error) {
	if spaceID == "" || tradingAccountID == "" || !key.Valid() ||
		(clientOrderID == "" && exchangeOrderID == "") {
		return OrderRecord{}, fmt.Errorf("%w: incomplete Fill order identity", ErrInvalidRecord)
	}
	var row orderRow
	query, args := queryOrderForFillByExchangeID, []any{spaceID, tradingAccountID, key.Market, key.Symbol, exchangeOrderID}
	if clientOrderID != "" && exchangeOrderID != "" {
		query, args = queryOrderForFillByEither, []any{spaceID, tradingAccountID, key.Market, key.Symbol, clientOrderID, exchangeOrderID}
	} else if clientOrderID != "" {
		query, args = queryOrderForFillByClientID, []any{spaceID, tradingAccountID, key.Market, key.Symbol, clientOrderID}
	}
	result := tx.db.Raw(query, args...).Scan(&row)
	if result.Error != nil {
		return OrderRecord{}, result.Error
	}
	if result.RowsAffected != 1 {
		return OrderRecord{}, fmt.Errorf("%w: Fill order not found", ErrInvalidRecord)
	}
	if clientOrderID != "" && row.ClientOrderID != clientOrderID {
		return OrderRecord{}, fmt.Errorf("%w: conflicting Fill client order ID", ErrConflict)
	}
	if exchangeOrderID != "" && row.ExchangeOrderID != "" &&
		row.ExchangeOrderID != exchangeOrderID {
		return OrderRecord{}, fmt.Errorf("%w: conflicting Fill Exchange order ID", ErrConflict)
	}
	return orderRecordFromRow(row), nil
}

// 成交找订单：按客户端订单号、交易所订单号或两者之一匹配，三条整句常量。
const (
	queryOrderForFillByEither = `SELECT * FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
		AND (c_client_order_id = ? OR c_exchange_order_id = ?)`
	queryOrderForFillByClientID = `SELECT * FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
		AND c_client_order_id = ?`
	queryOrderForFillByExchangeID = `SELECT * FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
		AND c_exchange_order_id = ?`
)

// UpdatePendingOrderReference must share the transaction with the following
// versioned order update, so a refreshed quote and reservation commit together.
func (tx *Tx) UpdatePendingOrderReference(record OrderRecord, expectedVersion uint64) error {
	price, err := shared.ParseDecimal(record.ReferencePrice)
	if err != nil || price.Cmp(shared.Zero()) <= 0 || record.ReferencePriceAt <= 0 {
		return fmt.Errorf("%w: invalid order reference", ErrInvalidRecord)
	}
	if record.PaperExecutionPrice != nil {
		paperPrice, err := shared.ParseDecimal(*record.PaperExecutionPrice)
		if err != nil || paperPrice.Cmp(shared.Zero()) <= 0 {
			return fmt.Errorf("%w: invalid paper execution price", ErrInvalidRecord)
		}
	}
	result := tx.db.Exec(`UPDATE t_trade_orders SET c_reference_price = ?, c_reference_price_at = ?, c_paper_execution_price = ?
		WHERE c_space_id = ? AND c_order_id = ? AND c_version = ? AND c_state = 'PENDING' AND c_submitted_at = 0`,
		price.String(), record.ReferencePriceAt, record.PaperExecutionPrice, record.SpaceID, record.OrderID, expectedVersion)
	if result.Error != nil {
		return writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func (tx *Tx) UpdateOrder(record OrderRecord, expectedVersion uint64) error {
	if record.SpaceID == "" || record.OrderID == "" || record.Version != expectedVersion+1 {
		return fmt.Errorf("%w: invalid order update", ErrInvalidRecord)
	}
	if record.State == "" {
		return fmt.Errorf("%w: empty order state", ErrInvalidRecord)
	}
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil || quantity.Cmp(shared.Zero()) <= 0 {
		return fmt.Errorf("%w: invalid order quantity", ErrInvalidRecord)
	}
	for label, value := range map[string]*string{
		"filled quantity":             &record.FilledQuantity,
		"average price":               &record.AveragePrice,
		"reserved quantity":           &record.ReservedQuantity,
		"remaining reserved quantity": &record.RemainingReservedQuantity,
	} {
		canonical, canonicalErr := canonicalDefaultZero(*value, label, decimalNonNegative)
		if canonicalErr != nil {
			return canonicalErr
		}
		*value = canonical
	}
	filled, _ := shared.ParseDecimal(record.FilledQuantity)
	reserved, _ := shared.ParseDecimal(record.ReservedQuantity)
	remaining, _ := shared.ParseDecimal(record.RemainingReservedQuantity)
	if filled.Cmp(quantity) > 0 || remaining.Cmp(reserved) > 0 {
		return fmt.Errorf("%w: inconsistent order quantities", ErrInvalidRecord)
	}
	result := tx.db.Exec(execUpdateOrderVersioned,
		record.ExchangeOrderID, record.State, record.Attempt, record.Quantity, record.FilledQuantity,
		record.AveragePrice, record.ReduceOnly, record.ReservedAsset,
		record.ReservedQuantity, record.RemainingReservedQuantity,
		record.RejectReason, record.ExchangeUpdatedAt, record.Version,
		record.SubmittedAt, record.FinishedAt, record.SpaceID, record.OrderID, expectedVersion,
	)
	if result.Error != nil {
		return writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: stale order version", ErrConflict)
	}
	return nil
}

func orderRecordFromRow(row orderRow) OrderRecord {
	return OrderRecord{
		SpaceID: row.SpaceID, OrderID: row.OrderID,
		TradingAccountID: row.TradingAccountID, ClientOrderID: row.ClientOrderID,
		ExchangeOrderID: row.ExchangeOrderID, Exchange: row.Exchange,
		MarketType: row.MarketType, InstrumentID: row.InstrumentID, ExchangeSymbol: row.ExchangeSymbol, OrderType: row.OrderType,
		TimeInForce: row.TimeInForce, Side: row.Side, PositionSide: row.PositionSide,
		Quantity: row.Quantity, LimitPrice: row.LimitPrice,
		ReferencePrice: row.ReferencePrice, ReferencePriceAt: row.ReferencePriceAt,
		ReduceOnly: row.ReduceOnly, OwnerType: row.OwnerType, OwnerID: row.OwnerID,
		LogicalAccountID: valueOrEmpty(row.LogicalAccountID),
		RunnerID:         valueOrEmpty(row.RunnerID), PositionBefore: row.PositionBefore, State: row.State,
		Attempt:        row.Attempt,
		FilledQuantity: row.FilledQuantity, AveragePrice: row.AveragePrice,
		ReservedAsset: row.ReservedAsset, ReservedQuantity: row.ReservedQuantity,
		RemainingReservedQuantity: row.RemainingReservedQuantity,
		PaperExecutionPrice:       row.PaperExecutionPrice, FirstMatchPending: row.FirstMatchPending,
		RejectReason: row.RejectReason, ExchangeUpdatedAt: row.ExchangeUpdatedAt,
		Version:     row.Version,
		SubmittedAt: row.SubmittedAt, FinishedAt: row.FinishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type FillRecord struct {
	SpaceID          string
	FillID           string
	ExchangeTradeID  string
	OrderID          string
	ExchangeOrderID  string
	TradingAccountID string
	Exchange         string
	MarketType       string
	ExchangeSymbol   string
	InstrumentID     string
	Side             string
	PositionSide     string
	Price            string
	Quantity         string
	Fee              string
	FeeAsset         string
	SettlementAsset  string
	RealizedPnL      string
	Role             string
	TradedAt         int64
	CreatedAt        time.Time
}

func (tx *Tx) InsertFill(record FillRecord) (bool, error) {
	if record.SpaceID == "" || record.FillID == "" || record.ExchangeTradeID == "" ||
		record.OrderID == "" || record.Price == "" || record.Quantity == "" {
		return false, fmt.Errorf("%w: incomplete Fill", ErrInvalidRecord)
	}
	var orderIdentity struct {
		TradingAccountID string `gorm:"column:c_trading_account_id"`
		ExchangeOrderID  string `gorm:"column:c_exchange_order_id"`
		Exchange         string `gorm:"column:c_exchange"`
		MarketType       string `gorm:"column:c_market_type"`
		InstrumentID     string `gorm:"column:c_instrument_id"`
		ExchangeSymbol   string `gorm:"column:c_exchange_symbol"`
		Side             string `gorm:"column:c_side"`
		PositionSide     string `gorm:"column:c_position_side"`
	}
	result := tx.db.Raw(`
		SELECT c_trading_account_id, c_exchange_order_id, c_exchange, c_market_type,
			c_instrument_id, c_exchange_symbol, c_side, c_position_side
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id = ?
	`, record.SpaceID, record.OrderID).Scan(&orderIdentity)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, fmt.Errorf("%w: Fill order identity", ErrInvalidRecord)
	}
	for _, values := range []struct {
		actual   *string
		expected string
		label    string
	}{
		{&record.TradingAccountID, orderIdentity.TradingAccountID, "Fill Exchange account"},
		{&record.Exchange, orderIdentity.Exchange, "Fill Exchange"},
		{&record.MarketType, orderIdentity.MarketType, "Fill market type"},
		{&record.InstrumentID, orderIdentity.InstrumentID, "Fill instrument ID"},
		{&record.ExchangeSymbol, orderIdentity.ExchangeSymbol, "Fill symbol"},
	} {
		if err := validateOrDeriveIdentity(values.actual, values.expected, values.label); err != nil {
			return false, err
		}
	}
	duplicate, err := tx.fillIdentityExists(record)
	if err != nil {
		return false, err
	}
	for _, values := range []struct {
		actual   *string
		expected string
		label    string
	}{
		{&record.Side, orderIdentity.Side, "Fill side"},
		{&record.PositionSide, orderIdentity.PositionSide, "Fill position side"},
	} {
		if err := validateOrDeriveIdentity(values.actual, values.expected, values.label); err != nil {
			if duplicate {
				return false, fmt.Errorf("%w: conflicting immutable %s", ErrConflict, values.label)
			}
			return false, err
		}
	}
	if record.ExchangeOrderID == "" {
		record.ExchangeOrderID = orderIdentity.ExchangeOrderID
	} else if orderIdentity.ExchangeOrderID != "" &&
		record.ExchangeOrderID != orderIdentity.ExchangeOrderID {
		return false, fmt.Errorf("%w: Fill Exchange order", ErrInvalidRecord)
	}
	if err := canonicalizeFill(&record); err != nil {
		return false, err
	}
	if duplicate {
		return tx.resolveDuplicateFill(record, ErrConflict)
	}
	result = tx.db.Exec(`
		INSERT INTO t_order_fills (
			c_space_id, c_fill_id, c_exchange_trade_id, c_order_id,
			c_exchange_order_id, c_trading_account_id, c_exchange, c_market_type,
			c_instrument_id, c_exchange_symbol, c_side, c_position_side, c_price, c_quantity, c_fee,
			c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.FillID, record.ExchangeTradeID, record.OrderID,
		record.ExchangeOrderID, record.TradingAccountID, record.Exchange,
		record.MarketType, record.InstrumentID, record.ExchangeSymbol, record.Side, record.PositionSide,
		record.Price, record.Quantity, record.Fee, record.FeeAsset,
		record.SettlementAsset, record.RealizedPnL, record.Role,
		record.TradedAt,
	)
	if result.Error != nil {
		conflict := writeError(result.Error)
		if errors.Is(conflict, ErrConflict) {
			return tx.resolveDuplicateFill(record, conflict)
		}
		return false, conflict
	}
	if err := tx.applyPaperFill(record); err != nil {
		return false, err
	}
	return true, nil
}

func (tx *Tx) fillIdentityExists(record FillRecord) (bool, error) {
	var count int64
	err := tx.db.Raw(queryCountOrderFills, record.SpaceID, record.TradingAccountID, record.MarketType, record.ExchangeSymbol, record.ExchangeTradeID).
		Scan(&count).Error
	return count != 0, err
}

type fillRow struct {
	SpaceID          string    `gorm:"column:c_space_id"`
	FillID           string    `gorm:"column:c_fill_id"`
	ExchangeTradeID  string    `gorm:"column:c_exchange_trade_id"`
	OrderID          string    `gorm:"column:c_order_id"`
	ExchangeOrderID  string    `gorm:"column:c_exchange_order_id"`
	TradingAccountID string    `gorm:"column:c_trading_account_id"`
	Exchange         string    `gorm:"column:c_exchange"`
	MarketType       string    `gorm:"column:c_market_type"`
	InstrumentID     string    `gorm:"column:c_instrument_id"`
	ExchangeSymbol   string    `gorm:"column:c_exchange_symbol"`
	Side             string    `gorm:"column:c_side"`
	PositionSide     string    `gorm:"column:c_position_side"`
	Price            string    `gorm:"column:c_price"`
	Quantity         string    `gorm:"column:c_quantity"`
	Fee              string    `gorm:"column:c_fee"`
	FeeAsset         string    `gorm:"column:c_fee_asset"`
	SettlementAsset  string    `gorm:"column:c_settlement_asset"`
	RealizedPnL      string    `gorm:"column:c_realized_pnl"`
	Role             string    `gorm:"column:c_role"`
	TradedAt         int64     `gorm:"column:c_traded_at"`
	CreatedAt        time.Time `gorm:"column:c_ctime"`
}

type FillQuery struct {
	TradingAccountID string
	LogicalAccountID string
	OrderID          string
	InstrumentID     string
	// Market 与 ExchangeSymbol 一起过滤；只给 ExchangeSymbol 不给 Market 会把同名的现货和合约混在一起。
	Market         string
	ExchangeSymbol string
	StartTime      int64
	EndTime        int64
	Offset         int
	Limit          int
}

func (s *Store) ListFills(
	ctx context.Context,
	spaceID string,
	query FillQuery,
) ([]FillRecord, int64, error) {
	if query.ExchangeSymbol != "" && query.Market == "" {
		return nil, 0, fmt.Errorf("%w: 按交易所代码查询必须同时给市场", ErrInvalidRecord)
	}
	db := s.db.WithContext(ctx).Table("t_order_fills").
		Where("c_space_id = ?", spaceID)
	if query.TradingAccountID != "" {
		db = db.Where("c_trading_account_id = ?", query.TradingAccountID)
	}
	// 按订单归属查询，已移出的成员的历史成交也算在组合账户下。
	if query.LogicalAccountID != "" {
		db = db.Where(`c_order_id IN (SELECT c_order_id FROM t_trade_orders WHERE c_space_id = ? AND c_logical_account_id = ?)`,
			spaceID, query.LogicalAccountID)
	}
	if query.OrderID != "" {
		db = db.Where("c_order_id = ?", query.OrderID)
	}
	if query.InstrumentID != "" {
		db = db.Where("c_instrument_id = ?", query.InstrumentID)
	}
	if query.Market != "" {
		db = db.Where("c_market_type = ?", query.Market)
	}
	if query.ExchangeSymbol != "" {
		db = db.Where("c_exchange_symbol = ?", query.ExchangeSymbol)
	}
	if query.StartTime > 0 {
		db = db.Where("c_traded_at >= ?", query.StartTime)
	}
	if query.EndTime > 0 {
		db = db.Where("c_traded_at <= ?", query.EndTime)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []fillRow
	if err := db.Order("c_traded_at DESC, c_fill_id DESC").
		Offset(query.Offset).Limit(query.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	records := make([]FillRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, FillRecord{
			SpaceID: row.SpaceID, FillID: row.FillID,
			ExchangeTradeID: row.ExchangeTradeID, OrderID: row.OrderID,
			ExchangeOrderID:  row.ExchangeOrderID,
			TradingAccountID: row.TradingAccountID, Exchange: row.Exchange,
			MarketType: row.MarketType, InstrumentID: row.InstrumentID, ExchangeSymbol: row.ExchangeSymbol, Side: row.Side,
			PositionSide: row.PositionSide, Price: row.Price, Quantity: row.Quantity,
			Fee: row.Fee, FeeAsset: row.FeeAsset,
			SettlementAsset: row.SettlementAsset, RealizedPnL: row.RealizedPnL,
			Role: row.Role, TradedAt: row.TradedAt, CreatedAt: row.CreatedAt,
		})
	}
	return records, total, nil
}

func (tx *Tx) resolveDuplicateFill(
	record FillRecord,
	conflict error,
) (bool, error) {
	var existing fillRow
	result := tx.db.Raw(queryFillByTradeID, record.SpaceID, record.TradingAccountID, record.MarketType, record.ExchangeSymbol, record.ExchangeTradeID).
		Scan(&existing)
	if result.Error != nil {
		return false, result.Error
	}
	want := fillRowFromRecord(record)
	canonicalReplay := strings.HasPrefix(record.FillID, record.TradingAccountID+":") &&
		strings.HasPrefix(existing.FillID, existing.TradingAccountID+":")
	if result.RowsAffected != 1 || (!canonicalReplay && existing.FillID != want.FillID) ||
		existing.SpaceID != want.SpaceID || existing.ExchangeTradeID != want.ExchangeTradeID ||
		existing.TradingAccountID != want.TradingAccountID || existing.Exchange != want.Exchange ||
		existing.MarketType != want.MarketType || existing.ExchangeSymbol != want.ExchangeSymbol ||
		existing.Side != want.Side || existing.PositionSide != want.PositionSide ||
		!sameDecimalText(existing.Price, want.Price) || !sameDecimalText(existing.Quantity, want.Quantity) {
		return false, fmt.Errorf("%w: conflicting immutable Fill replay", conflict)
	}
	if existing.OrderID != want.OrderID || existing.ExchangeOrderID != want.ExchangeOrderID {
		return false, fmt.Errorf("%w: conflicting immutable Fill order replay", conflict)
	}
	// 私有流和 REST 的手续费、手续费资产、成交角色、时间等字段可能不同（例如无手续费时私有流不带资产）。
	// 订单、方向、数量、价格一致就是同一笔成交，其余字段不一致只记日志，按重复处理。
	if existing.Fee != want.Fee || existing.FeeAsset != want.FeeAsset ||
		existing.SettlementAsset != want.SettlementAsset || existing.RealizedPnL != want.RealizedPnL ||
		existing.Role != want.Role || existing.TradedAt != want.TradedAt {
		log.Warnf("成交 %s 重放字段不一致，按重复处理：已有 fee=%s %s role=%s at=%d，新 fee=%s %s role=%s at=%d",
			record.ExchangeTradeID, existing.Fee, existing.FeeAsset, existing.Role, existing.TradedAt,
			want.Fee, want.FeeAsset, want.Role, want.TradedAt)
	}
	// A trade identity can only replay the same immutable facts and order.
	return false, nil
}

func fillRowFromRecord(record FillRecord) fillRow {
	return fillRow{
		SpaceID: record.SpaceID, FillID: record.FillID,
		ExchangeTradeID: record.ExchangeTradeID, OrderID: record.OrderID,
		ExchangeOrderID:  record.ExchangeOrderID,
		TradingAccountID: record.TradingAccountID, Exchange: record.Exchange,
		MarketType: record.MarketType, InstrumentID: record.InstrumentID, ExchangeSymbol: record.ExchangeSymbol, Side: record.Side,
		PositionSide: record.PositionSide, Price: record.Price,
		Quantity: record.Quantity, Fee: record.Fee, FeeAsset: record.FeeAsset,
		SettlementAsset: record.SettlementAsset, RealizedPnL: record.RealizedPnL,
		Role: record.Role, TradedAt: record.TradedAt,
	}
}

type tradingAccountIdentity struct {
	Exchange      string
	Markets       []string
	Environment   string
	ExecutionMode string
}

// resolveMarket 校验或推导订单、持仓的市场：必须是账户已开通的市场；没写市场时，只有单市场账户才能推导。
func (i tradingAccountIdentity) resolveMarket(market *string, label string) error {
	if *market == "" {
		if len(i.Markets) != 1 {
			return fmt.Errorf("%w: 账户开通了多个市场，%s必须指定市场", ErrInvalidRecord, label)
		}
		*market = i.Markets[0]
		return nil
	}
	if !slices.Contains(i.Markets, *market) {
		return fmt.Errorf("%w: 账户没有开通市场 %s（已开通 %v）", ErrInvalidRecord, *market, i.Markets)
	}
	return nil
}

// accountIdentity 返回账户的交易所、已开通市场和行情环境。账户创建后这些不变，按账户缓存。
func (tx *Tx) accountIdentity(spaceID string, tradingAccountID string) (tradingAccountIdentity, error) {
	if tx.store == nil || tx.accountsWritten {
		return tx.loadAccountIdentity(spaceID, tradingAccountID)
	}
	return tx.store.identities.Get(accountKey{spaceID, tradingAccountID}, func() (tradingAccountIdentity, error) {
		return tx.loadAccountIdentity(spaceID, tradingAccountID)
	})
}

func (tx *Tx) loadAccountIdentity(spaceID string, tradingAccountID string) (tradingAccountIdentity, error) {
	var row struct {
		Exchange      string `gorm:"column:c_exchange"`
		Markets       string `gorm:"column:c_markets"`
		Environment   string `gorm:"column:c_environment"`
		ExecutionMode string `gorm:"column:c_execution_mode"`
	}
	result := tx.db.Raw(queryAccountIdentity, spaceID, tradingAccountID).Scan(&row)
	if result.Error != nil {
		return tradingAccountIdentity{}, result.Error
	}
	if result.RowsAffected != 1 {
		return tradingAccountIdentity{}, fmt.Errorf("%w: Exchange account identity", ErrInvalidRecord)
	}
	markets, err := decodeMarkets(row.Markets)
	if err != nil {
		return tradingAccountIdentity{}, err
	}
	identity := tradingAccountIdentity{
		Exchange: row.Exchange, Markets: markets, Environment: row.Environment, ExecutionMode: row.ExecutionMode,
	}
	if identity.ExecutionMode == "PAPER" {
		identity.Environment = "PRODUCTION"
	}
	if identity.Environment == "" {
		return tradingAccountIdentity{}, fmt.Errorf("%w: missing account environment", ErrInvalidRecord)
	}
	// identity is normalized to the market-data environment for instrument lookup.
	return identity, nil
}

func validateOrDeriveIdentity(actual *string, expected string, label string) error {
	if *actual == "" {
		*actual = expected
		return nil
	}
	if *actual != expected {
		return fmt.Errorf("%w: contradictory %s", ErrInvalidRecord, label)
	}
	return nil
}

type PositionRecord struct {
	SpaceID           string
	TradingAccountID  string
	MarketType        string
	InstrumentID      string
	ExchangeSymbol    string
	PositionSide      string
	SignedQuantity    string
	EntryPrice        string
	MarkPrice         string
	Leverage          string
	MarginMode        string
	UsedMargin        string
	LiquidationPrice  string
	UnrealizedPnL     string
	RealizedPnL       string
	ExchangeUpdatedAt int64
	UpdatedAt         time.Time
}

func (tx *Tx) UpsertPosition(record PositionRecord) error {
	return tx.upsertPosition(record, 0)
}

// upsertPosition 写入持仓：交易所时间不早于本地行时覆盖；本地行时间不晚于 overrideBefore（已过保护期）时也覆盖，
// 这样 REST 持仓过了保护期后以 REST 为准，不再比较时间。
func (tx *Tx) upsertPosition(record PositionRecord, overrideBefore int64) error {
	if record.SpaceID == "" || record.TradingAccountID == "" || record.ExchangeSymbol == "" ||
		record.PositionSide == "" {
		return fmt.Errorf("%w: incomplete position", ErrInvalidRecord)
	}
	identity, err := tx.accountIdentity(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if err := identity.resolveMarket(&record.MarketType, "持仓"); err != nil {
		return err
	}
	if record.InstrumentID == "" {
		set, err := tx.instrumentSet(instrumentScope{identity.Exchange, identity.Environment, record.MarketType})
		if err != nil {
			return err
		}
		if instrument, lookupErr := set.symbol(record.ExchangeSymbol); lookupErr == nil && instrument.InstrumentID != "" {
			record.InstrumentID = instrument.InstrumentID
		} else {
			record.InstrumentID = record.ExchangeSymbol
		}
	}
	if err := canonicalizePosition(&record, record.MarketType); err != nil {
		return err
	}
	return tx.db.Exec(execUpsertPosition,
		record.SpaceID, record.TradingAccountID, record.MarketType, record.InstrumentID, record.ExchangeSymbol, record.PositionSide,
		record.SignedQuantity, record.EntryPrice,
		record.MarkPrice, record.Leverage, record.MarginMode,
		record.UsedMargin, record.LiquidationPrice,
		record.UnrealizedPnL, record.RealizedPnL,
		record.ExchangeUpdatedAt, overrideBefore,
	).Error
}

// ReplacePositionsForAccount 用 REST 持仓快照替换本地持仓。protectAfter 是持仓滞后保护期的起点（毫秒）：
// 本地行时间晚于它的不删，也不被更旧的 REST 覆盖，避免持仓接口落后时删掉刚成交投影出的持仓；
// 早于它的行以 REST 为准。protectAfter 为 0 时不保护。
func (tx *Tx) ReplacePositionsForAccount(
	spaceID string,
	tradingAccountID string,
	records []PositionRecord,
	snapshotAt int64,
	protectAfter int64,
) error {
	if blank(spaceID) || blank(tradingAccountID) || snapshotAt <= 0 {
		return fmt.Errorf("%w: incomplete position snapshot identity", ErrInvalidRecord)
	}
	identity, err := tx.accountIdentity(spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	// 先确定每条持仓的市场：后面保留、墓碑的匹配都按（市场，代码，方向）。
	records = append([]PositionRecord(nil), records...)
	for i := range records {
		if err := identity.resolveMarket(&records[i].MarketType, "持仓"); err != nil {
			return err
		}
	}
	for _, record := range records {
		if record.SpaceID != spaceID || record.TradingAccountID != tradingAccountID {
			return fmt.Errorf("%w: conflicting position snapshot identity", ErrInvalidRecord)
		}
		if err := tx.upsertPosition(record, protectAfter); err != nil {
			return err
		}
		if err := tx.markPositionSnapshotCovered(record); err != nil {
			return err
		}
	}
	deleteBefore := snapshotAt
	if protectAfter > 0 && protectAfter < deleteBefore {
		deleteBefore = protectAfter
	}
	// 快照里仍然存在的持仓用 json_each(?) 传入，整句固定，不拼接占位符。
	retained := make([]positionKeyJSON, 0, len(records))
	for _, record := range records {
		retained = append(retained, positionKeyJSON{Market: record.MarketType, Symbol: record.ExchangeSymbol, Side: record.PositionSide})
	}
	retainedJSON, err := json.Marshal(retained)
	if err != nil {
		return fmt.Errorf("%w: encode retained positions", ErrInvalidRecord)
	}
	args := []any{spaceID, tradingAccountID, deleteBefore, string(retainedJSON)}
	// 被删除的持仓留下墓碑：覆盖时间取这次快照的边界和保护期起点中较晚者（含已有行的覆盖时间），
	// 迟到的平仓成交不能从零叠加出反向持仓。保护期起点之前的成交已经反映在交易所持仓里；
	// 只用快照边界的话，边界会被本轮最早成交压低，没拉到成交的标的会被后来的平仓成交投影成反向持仓。
	tombstoneAt := snapshotAt
	if protectAfter > tombstoneAt {
		tombstoneAt = protectAfter
	}
	tombstoneArgs := append([]any{tombstoneAt}, args...)
	if err := tx.db.Exec(execTombstoneReplacedPositions, tombstoneArgs...).Error; err != nil {
		return writeError(err)
	}
	// 快照里仍然存在的持仓不再需要墓碑
	for _, record := range records {
		if err := tx.db.Exec(execDeletePositionTombstone,
			record.SpaceID, record.TradingAccountID, record.MarketType, record.ExchangeSymbol, record.PositionSide,
		).Error; err != nil {
			return writeError(err)
		}
	}
	if err := tx.db.Exec(execDeleteReplacedPositions, args...).Error; err != nil {
		return writeError(err)
	}
	return nil
}

// positionKeyJSON 是持仓快照替换时保留的一条持仓键，经 json_each(?) 传给 SQL。
type positionKeyJSON struct {
	Market string `json:"m"`
	Symbol string `json:"s"`
	Side   string `json:"p"`
}

// 持仓快照替换：删除（并留墓碑）账户里不晚于边界、且不在快照保留键里的持仓。参数依次是
// 空间、执行账户、边界时间、保留键 JSON 数组（空数组表示不保留）；留墓碑的语句前面多一个覆盖时间。
const (
	replacedPositionsFilter = `c_space_id = ? AND c_trading_account_id = ? AND c_exchange_updated_at <= ?
		AND NOT EXISTS (
			SELECT 1 FROM json_each(?) AS kept
			WHERE json_extract(kept.value, '$.m') = t_trading_positions.c_market_type
				AND json_extract(kept.value, '$.s') = t_trading_positions.c_exchange_symbol
				AND json_extract(kept.value, '$.p') = t_trading_positions.c_position_side
		)`
	execTombstoneReplacedPositions = `INSERT INTO t_trading_position_tombstones (c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol, c_position_side, c_covered_until)
		SELECT c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol, c_position_side, MAX(c_snapshot_covered_until, ?)
		FROM t_trading_positions
		WHERE ` + replacedPositionsFilter + `
		ON CONFLICT(c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol, c_position_side)
		DO UPDATE SET c_covered_until = MAX(c_covered_until, excluded.c_covered_until)`
	execDeleteReplacedPositions = `DELETE FROM t_trading_positions WHERE ` + replacedPositionsFilter
	execDeletePositionTombstone = `DELETE FROM t_trading_position_tombstones
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_position_side = ?`
)

// markPositionSnapshotCovered 记录权威持仓快照已覆盖到的时间（只升不降）。
func (tx *Tx) markPositionSnapshotCovered(record PositionRecord) error {
	return writeError(tx.db.Exec(execMarkPositionSnapshotCovered, record.ExchangeUpdatedAt, record.SpaceID, record.TradingAccountID, record.MarketType, record.ExchangeSymbol, record.PositionSide).Error)
}

// PositionSnapshotCoveredUntil 返回权威持仓快照已覆盖到的时间；没有快照覆盖过返回 0。
func (tx *Tx) PositionSnapshotCoveredUntil(spaceID, tradingAccountID string, key exchange.SymbolKey, positionSide string) (int64, error) {
	var covered int64
	if err := tx.db.Raw(queryPositionSnapshotCoveredUntil, spaceID, tradingAccountID, key.Market, key.Symbol, positionSide, spaceID, tradingAccountID, key.Market, key.Symbol, positionSide).Scan(&covered).Error; err != nil {
		return 0, err
	}
	return covered, nil
}

func (tx *Tx) GetPosition(
	spaceID string,
	tradingAccountID string,
	key exchange.SymbolKey,
	positionSide string,
) (PositionRecord, bool, error) {
	var row struct {
		SpaceID           string    `gorm:"column:c_space_id"`
		TradingAccountID  string    `gorm:"column:c_trading_account_id"`
		MarketType        string    `gorm:"column:c_market_type"`
		InstrumentID      string    `gorm:"column:c_instrument_id"`
		ExchangeSymbol    string    `gorm:"column:c_exchange_symbol"`
		PositionSide      string    `gorm:"column:c_position_side"`
		SignedQuantity    string    `gorm:"column:c_signed_quantity"`
		EntryPrice        string    `gorm:"column:c_entry_price"`
		MarkPrice         string    `gorm:"column:c_mark_price"`
		Leverage          string    `gorm:"column:c_leverage"`
		MarginMode        string    `gorm:"column:c_margin_mode"`
		UsedMargin        string    `gorm:"column:c_used_margin"`
		LiquidationPrice  string    `gorm:"column:c_liquidation_price"`
		UnrealizedPnL     string    `gorm:"column:c_unrealized_pnl"`
		RealizedPnL       string    `gorm:"column:c_realized_pnl"`
		ExchangeUpdatedAt int64     `gorm:"column:c_exchange_updated_at"`
		UpdatedAt         time.Time `gorm:"column:c_mtime"`
	}
	result := tx.db.Raw(queryPosition, spaceID, tradingAccountID, key.Market, key.Symbol, positionSide).Scan(&row)
	if result.Error != nil {
		return PositionRecord{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return PositionRecord{}, false, nil
	}
	return PositionRecord{
		SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID, MarketType: row.MarketType, InstrumentID: row.InstrumentID,
		ExchangeSymbol: row.ExchangeSymbol, PositionSide: row.PositionSide,
		SignedQuantity: row.SignedQuantity, EntryPrice: row.EntryPrice,
		MarkPrice: row.MarkPrice, Leverage: row.Leverage,
		MarginMode: row.MarginMode, UsedMargin: row.UsedMargin,
		LiquidationPrice: row.LiquidationPrice,
		UnrealizedPnL:    row.UnrealizedPnL, RealizedPnL: row.RealizedPnL,
		ExchangeUpdatedAt: row.ExchangeUpdatedAt,
		UpdatedAt:         row.UpdatedAt,
	}, true, nil
}

func (s *Store) GetPosition(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	key exchange.SymbolKey,
	positionSide string,
) (PositionRecord, bool, error) {
	var record PositionRecord
	var found bool
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		record, found, err = tx.GetPosition(
			spaceID,
			tradingAccountID,
			key,
			positionSide,
		)
		return err
	})
	return record, found, err
}

// ListPositions 列出账户的持仓；key 为零值时列出全部，否则只列这个（市场、交易所代码）下的持仓。
func (s *Store) ListPositions(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	key exchange.SymbolKey,
) ([]PositionRecord, error) {
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	return s.listPositions(ctx, spaceID, tradingAccountID, func(db *gorm.DB) *gorm.DB {
		if key.IsZero() {
			return db
		}
		return db.Where("c_market_type = ? AND c_exchange_symbol = ?", key.Market, key.Symbol)
	})
}

// ListPositionsByInstrument filters positions by the canonical instrument ID.
func (s *Store) ListPositionsByInstrument(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	instrumentID string,
) ([]PositionRecord, error) {
	return s.listPositions(ctx, spaceID, tradingAccountID, func(db *gorm.DB) *gorm.DB {
		if instrumentID == "" {
			return db
		}
		return db.Where("c_instrument_id = ?", instrumentID)
	})
}

func (s *Store) listPositions(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	filter func(*gorm.DB) *gorm.DB,
) ([]PositionRecord, error) {
	db := filter(s.db.WithContext(ctx).Table("t_trading_positions").
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID))
	var rows []struct {
		SpaceID           string    `gorm:"column:c_space_id"`
		TradingAccountID  string    `gorm:"column:c_trading_account_id"`
		MarketType        string    `gorm:"column:c_market_type"`
		InstrumentID      string    `gorm:"column:c_instrument_id"`
		ExchangeSymbol    string    `gorm:"column:c_exchange_symbol"`
		PositionSide      string    `gorm:"column:c_position_side"`
		SignedQuantity    string    `gorm:"column:c_signed_quantity"`
		EntryPrice        string    `gorm:"column:c_entry_price"`
		MarkPrice         string    `gorm:"column:c_mark_price"`
		Leverage          string    `gorm:"column:c_leverage"`
		MarginMode        string    `gorm:"column:c_margin_mode"`
		UsedMargin        string    `gorm:"column:c_used_margin"`
		LiquidationPrice  string    `gorm:"column:c_liquidation_price"`
		UnrealizedPnL     string    `gorm:"column:c_unrealized_pnl"`
		RealizedPnL       string    `gorm:"column:c_realized_pnl"`
		ExchangeUpdatedAt int64     `gorm:"column:c_exchange_updated_at"`
		UpdatedAt         time.Time `gorm:"column:c_mtime"`
	}
	if err := db.Order("c_market_type, c_exchange_symbol, c_position_side").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]PositionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, PositionRecord{
			SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID, MarketType: row.MarketType,
			InstrumentID: row.InstrumentID, ExchangeSymbol: row.ExchangeSymbol, PositionSide: row.PositionSide,
			SignedQuantity: row.SignedQuantity, EntryPrice: row.EntryPrice,
			MarkPrice: row.MarkPrice, Leverage: row.Leverage,
			MarginMode: row.MarginMode, UsedMargin: row.UsedMargin,
			LiquidationPrice: row.LiquidationPrice,
			UnrealizedPnL:    row.UnrealizedPnL, RealizedPnL: row.RealizedPnL,
			ExchangeUpdatedAt: row.ExchangeUpdatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return records, nil
}

func canonicalizeOrder(record *OrderRecord) error {
	var err error
	record.Quantity, err = canonicalDecimal(record.Quantity, "order quantity", decimalPositive)
	if err != nil {
		return err
	}
	record.ReferencePrice, err = canonicalDecimal(
		record.ReferencePrice,
		"order reference price",
		decimalPositive,
	)
	if err != nil {
		return err
	}
	if record.LimitPrice != nil {
		value, err := canonicalDecimal(*record.LimitPrice, "order limit price", decimalPositive)
		if err != nil {
			return err
		}
		record.LimitPrice = &value
	}
	if record.OrderType == "LIMIT" && record.LimitPrice == nil {
		return fmt.Errorf("%w: LIMIT order price", ErrInvalidRecord)
	}
	if record.OrderType == "MARKET" && record.LimitPrice != nil {
		return fmt.Errorf("%w: MARKET order price", ErrInvalidRecord)
	}
	for _, field := range []struct {
		value *string
		label string
	}{
		{&record.FilledQuantity, "filled quantity"},
		{&record.AveragePrice, "average price"},
		{&record.ReservedQuantity, "reserved quantity"},
		{&record.RemainingReservedQuantity, "remaining reserved quantity"},
	} {
		*field.value, err = canonicalDefaultZero(*field.value, field.label, decimalNonNegative)
		if err != nil {
			return err
		}
	}
	if decimalGreater(record.FilledQuantity, record.Quantity) ||
		decimalGreater(record.RemainingReservedQuantity, record.ReservedQuantity) {
		return fmt.Errorf("%w: inconsistent order totals", ErrInvalidRecord)
	}
	return nil
}

func canonicalizeFill(record *FillRecord) error {
	var err error
	record.Price, err = canonicalDecimal(record.Price, "Fill price", decimalPositive)
	if err != nil {
		return err
	}
	record.Quantity, err = canonicalDecimal(record.Quantity, "Fill quantity", decimalPositive)
	if err != nil {
		return err
	}
	record.Fee, err = canonicalDefaultZero(record.Fee, "Fill fee", decimalSigned)
	if err != nil {
		return err
	}
	record.RealizedPnL, err = canonicalDefaultZero(
		record.RealizedPnL,
		"Fill realized PnL",
		decimalSigned,
	)
	return err
}

func canonicalizePosition(record *PositionRecord, marketType string) error {
	var err error
	for _, field := range []struct {
		value *string
		label string
		sign  decimalSign
	}{
		{&record.SignedQuantity, "position signed quantity", decimalSigned},
		{&record.EntryPrice, "position entry price", decimalNonNegative},
		{&record.MarkPrice, "position mark price", decimalNonNegative},
		{&record.UsedMargin, "position used margin", decimalNonNegative},
		{&record.LiquidationPrice, "position liquidation price", decimalNonNegative},
		{&record.UnrealizedPnL, "position unrealized PnL", decimalSigned},
		{&record.RealizedPnL, "position realized PnL", decimalSigned},
	} {
		*field.value, err = canonicalDefaultZero(*field.value, field.label, field.sign)
		if err != nil {
			return err
		}
	}
	leverageSign := decimalNonNegative
	if exchange.RuleFor(marketType).Leverage {
		leverageSign = decimalPositive
	}
	record.Leverage, err = canonicalDefaultZero(
		record.Leverage,
		"position leverage",
		leverageSign,
	)
	return err
}

func decimalGreater(left string, right string) bool {
	leftValue, _ := shared.ParseDecimal(left)
	rightValue, _ := shared.ParseDecimal(right)
	return leftValue.Cmp(rightValue) > 0
}

// sameDecimalText 比较两个十进制文本的数值，忽略 0.10 和 0.1 这类写法差异。
func sameDecimalText(left, right string) bool {
	if left == right {
		return true
	}
	a, errA := shared.ParseDecimal(left)
	b, errB := shared.ParseDecimal(right)
	return errA == nil && errB == nil && a.Cmp(b) == 0
}

// TargetFillNotionalSince 返回执行账户 since 之后目标子单的成交额（价格 × 数量），清仓和人工单不算。
func (s *Store) TargetFillNotionalSince(ctx context.Context, spaceID, tradingAccountID string, since int64) (float64, error) {
	var total struct {
		Notional float64 `gorm:"column:notional"`
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(CAST(f.c_price AS REAL) * CAST(f.c_quantity AS REAL)), 0) AS notional
		FROM t_order_fills AS f
		JOIN t_trade_orders AS o ON o.c_space_id = f.c_space_id AND o.c_order_id = f.c_order_id
		WHERE f.c_space_id = ? AND f.c_trading_account_id = ? AND f.c_traded_at > ?
			AND o.c_owner_type = 'TARGET'
	`, spaceID, tradingAccountID, since).Scan(&total).Error
	return total.Notional, err
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	// 新建订单。
	execInsertOrder = `INSERT INTO t_trade_orders (
			c_space_id, c_order_id, c_trading_account_id, c_client_order_id,
			c_exchange_order_id, c_exchange, c_market_type, c_instrument_id, c_exchange_symbol, c_order_type,
			c_time_in_force, c_side, c_position_side, c_quantity, c_limit_price,
			c_reference_price, c_reference_price_at, c_reduce_only, c_owner_type,
			c_owner_id, c_logical_account_id, c_runner_id, c_position_before,
			c_state, c_attempt, c_filled_quantity, c_average_price,
			c_reserved_asset, c_reserved_quantity, c_remaining_reserved_quantity,
			c_paper_execution_price, c_first_match_pending, c_reject_reason,
			c_exchange_updated_at, c_version, c_submitted_at, c_finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	// 按版本号更新订单。
	execUpdateOrderVersioned = `UPDATE t_trade_orders
		SET c_exchange_order_id = ?, c_state = ?, c_attempt = ?, c_quantity = ?, c_filled_quantity = ?,
			c_average_price = ?, c_reduce_only = ?, c_reserved_asset = ?,
			c_reserved_quantity = ?, c_remaining_reserved_quantity = ?,
			c_reject_reason = ?, c_exchange_updated_at = ?, c_version = ?,
			c_submitted_at = ?, c_finished_at = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_order_id = ? AND c_version = ?`
	queryCountOrderFills = `SELECT COUNT(*) FROM t_order_fills
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
			AND c_exchange_trade_id = ?`
	queryFillByTradeID = `SELECT c_space_id, c_fill_id, c_exchange_trade_id, c_order_id,
			c_exchange_order_id, c_trading_account_id, c_exchange, c_market_type,
			c_instrument_id, c_exchange_symbol, c_side, c_position_side, c_price, c_quantity, c_fee,
			c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at
		FROM t_order_fills
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
			AND c_exchange_trade_id = ?`
	queryAccountIdentity = `SELECT c_exchange, c_markets, c_live_environment AS c_environment, c_execution_mode
		FROM t_trading_accounts
		WHERE c_space_id = ? AND c_trading_account_id = ?`
	execUpsertPosition = `INSERT INTO t_trading_positions (
			c_space_id, c_trading_account_id, c_market_type, c_instrument_id, c_exchange_symbol, c_position_side,
			c_signed_quantity, c_entry_price, c_mark_price, c_leverage, c_margin_mode,
			c_used_margin, c_liquidation_price, c_unrealized_pnl, c_realized_pnl,
			c_exchange_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol, c_position_side)
		DO UPDATE SET
			c_signed_quantity = excluded.c_signed_quantity,
			c_entry_price = excluded.c_entry_price,
			c_mark_price = excluded.c_mark_price,
			c_leverage = excluded.c_leverage,
			c_margin_mode = excluded.c_margin_mode,
			c_used_margin = excluded.c_used_margin,
			c_liquidation_price = excluded.c_liquidation_price,
			c_unrealized_pnl = excluded.c_unrealized_pnl,
			c_realized_pnl = excluded.c_realized_pnl,
			c_exchange_updated_at = excluded.c_exchange_updated_at,
			c_mtime = CURRENT_TIMESTAMP
		WHERE excluded.c_exchange_updated_at >=
			t_trading_positions.c_exchange_updated_at
			OR t_trading_positions.c_exchange_updated_at <= ?`
	execMarkPositionSnapshotCovered = `UPDATE t_trading_positions
		SET c_snapshot_covered_until = MAX(c_snapshot_covered_until, ?)
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_position_side = ?`
	queryPositionSnapshotCoveredUntil = `SELECT MAX(
			COALESCE((SELECT MAX(c_snapshot_covered_until) FROM t_trading_positions
				WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_position_side = ?), 0),
			COALESCE((SELECT MAX(c_covered_until) FROM t_trading_position_tombstones
				WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_position_side = ?), 0)
		)`
	queryPosition = `SELECT * FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ?
			AND c_exchange_symbol = ? AND c_position_side = ?`
)

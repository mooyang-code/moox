package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

const (
	smokeSpaceID = "trade-testnet-smoke"
	confirmEnv   = "MOOX_TRADE_TESTNET_CONFIRM"
)

type smokeOptions struct {
	Phase          string
	Exchange       exchange.Exchange
	Environment    exchange.AccountEnvironment
	SecretID       string
	Database       string
	State          string
	Config         string
	ExchangeSymbol string
	MaxNotional    shared.Decimal
	MarketType     exchange.MarketType
}

func parseOptions(args []string, getenv func(string) string) (smokeOptions, error) {
	var options smokeOptions
	flags := flag.NewFlagSet("testnet-smoke", flag.ContinueOnError)
	var exchangeName string
	var maxNotional string
	var market, environment string
	flags.StringVar(&options.Phase, "phase", "", "submit, recover or facts")
	flags.StringVar(&market, "market", "SPOT", "market to exercise: SPOT or SWAP (币安现货、合约测试网是两套域名和两把密钥，分别用各自的 --secret-id 各跑一次)")
	flags.StringVar(&environment, "environment", "TESTNET", "facts phase: TESTNET or PRODUCTION")
	flags.StringVar(&exchangeName, "exchange", "", "BINANCE or OKX")
	flags.StringVar(&options.SecretID, "secret-id", "", "Admin Exchange Secret ID")
	flags.StringVar(&options.Database, "database", "", "isolated Trade SQLite database")
	flags.StringVar(&options.State, "state", "", "restart state file")
	flags.StringVar(&options.Config, "config", "modules/trade/config/app.yaml", "Trade app config")
	flags.StringVar(&options.ExchangeSymbol, "exchange-symbol", "", "testnet native exchange symbol")
	flags.StringVar(&maxNotional, "max-notional", "20", "maximum test order quote notional")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	options.Phase = strings.ToLower(strings.TrimSpace(options.Phase))
	readOnly := options.Phase == "facts"
	if !readOnly && (getenv == nil || getenv(confirmEnv) != "YES") {
		return options, fmt.Errorf("SKIP: refusing real orders without %s=YES", confirmEnv)
	}
	if !readOnly && options.Phase != "submit" && options.Phase != "recover" {
		return options, errors.New("phase must be submit, recover or facts")
	}
	options.MarketType = exchange.MarketType(strings.ToUpper(strings.TrimSpace(market)))
	if options.MarketType != exchange.MarketTypeSpot && options.MarketType != exchange.MarketTypeSwap {
		return options, errors.New("market must be SPOT or SWAP")
	}
	options.Exchange = exchange.Exchange(strings.ToUpper(strings.TrimSpace(exchangeName)))
	if options.Exchange != exchange.ExchangeBinance &&
		options.Exchange != exchange.ExchangeOKX {
		return options, errors.New("exchange must be BINANCE or OKX")
	}
	options.Environment = exchange.AccountEnvironmentTestnet
	if strings.TrimSpace(options.SecretID) == "" {
		return options, errors.New("SKIP: Exchange Testnet secret ID is required")
	}
	if readOnly {
		// 只读阶段不写库也不下单，可以用生产密钥核对真实接口。
		options.Environment = exchange.AccountEnvironment(strings.ToUpper(strings.TrimSpace(environment)))
		return options, nil
	}
	if strings.TrimSpace(options.Database) == "" {
		return options, errors.New("isolated Trade database path is required")
	}
	if strings.TrimSpace(options.State) == "" {
		return options, errors.New("restart state path is required")
	}
	if options.ExchangeSymbol == "" {
		switch {
		case options.Exchange == exchange.ExchangeBinance:
			options.ExchangeSymbol = "BTCUSDT"
		case options.MarketType == exchange.MarketTypeSwap:
			options.ExchangeSymbol = "BTC-USDT-SWAP"
		default:
			options.ExchangeSymbol = "BTC-USDT"
		}
	}
	var err error
	options.MaxNotional, err = shared.ParseDecimal(maxNotional)
	if err != nil || options.MaxNotional.Cmp(shared.Zero()) <= 0 {
		return options, errors.New("max-notional must be a positive decimal")
	}
	return options, nil
}

// passiveOrder 是被动测试单：价格比参考价低 10%，不会成交。Quantity 是基础资产数量（下单命令用的单位）。
type passiveOrder struct {
	Price    shared.Decimal
	Quantity shared.Decimal
}

// planPassiveBuy 按交易所规则规划被动买单。quantityStep、minQuantity 是交易所数量单位（OKX 永续是张），
// contractValue 是每张折合的基础资产数量（现货和币安合约为 1，缺失按 1）；名义金额 = 价格 × 数量 × 合约面值，
// 不能漏掉合约面值，否则永续的安全上限差几个数量级。
func planPassiveBuy(
	reference shared.Decimal,
	priceTick shared.Decimal,
	quantityStep shared.Decimal,
	minQuantity shared.Decimal,
	minNotional shared.Decimal,
	contractValue shared.Decimal,
	maxNotional shared.Decimal,
) (passiveOrder, error) {
	if reference.Cmp(shared.Zero()) <= 0 ||
		priceTick.Cmp(shared.Zero()) <= 0 ||
		quantityStep.Cmp(shared.Zero()) <= 0 ||
		minQuantity.Cmp(shared.Zero()) <= 0 ||
		maxNotional.Cmp(shared.Zero()) <= 0 {
		return passiveOrder{}, errors.New("invalid test order constraints")
	}
	if contractValue.Cmp(shared.Zero()) <= 0 {
		contractValue = shared.MustDecimal("1")
	}
	price := floorToStep(reference.Mul(shared.MustDecimal("0.9")), priceTick)
	if price.Cmp(shared.Zero()) <= 0 {
		return passiveOrder{}, errors.New("passive test price rounded to zero")
	}
	quantity := ceilToStep(minQuantity, quantityStep)
	if minNotional.Cmp(shared.Zero()) > 0 {
		required := ceilToStep(minNotional.Div(price.Mul(contractValue)), quantityStep)
		if required.Cmp(quantity) > 0 {
			quantity = required
		}
	}
	base := quantity.Mul(contractValue)
	notional := price.Mul(base)
	if notional.Cmp(maxNotional) > 0 {
		return passiveOrder{}, fmt.Errorf(
			"minimum test order notional %s exceeds safety cap %s",
			notional,
			maxNotional,
		)
	}
	return passiveOrder{Price: price, Quantity: base}, nil
}

func floorToStep(value, step shared.Decimal) shared.Decimal {
	ratio := value.Div(step)
	rat := new(big.Rat)
	rat.SetString(ratio.String())
	quotient := new(big.Int).Quo(rat.Num(), rat.Denom())
	return step.Mul(shared.MustDecimal(quotient.String()))
}

func ceilToStep(value, step shared.Decimal) shared.Decimal {
	ratio := value.Div(step)
	rat := new(big.Rat)
	rat.SetString(ratio.String())
	quotient, remainder := new(big.Int).QuoRem(
		rat.Num(),
		rat.Denom(),
		new(big.Int),
	)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return step.Mul(shared.MustDecimal(quotient.String()))
}

type smokeState struct {
	Version           int                         `json:"version"`
	Exchange          exchange.Exchange           `json:"exchange"`
	Environment       exchange.AccountEnvironment `json:"environment"`
	MarketType        exchange.MarketType         `json:"market_type"`
	SpaceID           string                      `json:"space_id"`
	AccountID         string                      `json:"account_id"`
	LogicalAccountID  string                      `json:"logical_account_id"`
	InstrumentID      string                      `json:"instrument_id"`
	ExchangeSymbol    string                      `json:"exchange_symbol"`
	BaseAsset         string                      `json:"base_asset"`
	BaselineBaseTotal string                      `json:"baseline_base_total"`
	ClientOrderID     string                      `json:"client_order_id"`
	OrderID           string                      `json:"order_id"`
	ExchangeOrderID   string                      `json:"exchange_order_id"`
}

func writeState(path string, state smokeState) error {
	if err := validateState(state, state.Exchange, state.MarketType); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".testnet-smoke-state-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readState(path string, expected exchange.Exchange, market exchange.MarketType) (smokeState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return smokeState{}, err
	}
	var state smokeState
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return smokeState{}, err
	}
	if err := validateState(state, expected, market); err != nil {
		return smokeState{}, err
	}
	return state, nil
}

func validateState(state smokeState, expected exchange.Exchange, market exchange.MarketType) error {
	if state.Version != 1 {
		return errors.New("unsupported smoke state version")
	}
	if state.Environment != exchange.AccountEnvironmentTestnet {
		return errors.New("smoke state must be TESTNET")
	}
	if state.Exchange != expected {
		return errors.New("smoke state exchange mismatch")
	}
	if state.MarketType != market || (market != exchange.MarketTypeSpot && market != exchange.MarketTypeSwap) {
		return errors.New("smoke state market mismatch")
	}
	if state.SpaceID != smokeSpaceID ||
		strings.TrimSpace(state.AccountID) == "" ||
		strings.TrimSpace(state.LogicalAccountID) == "" ||
		strings.TrimSpace(state.InstrumentID) == "" ||
		strings.TrimSpace(state.ExchangeSymbol) == "" ||
		strings.TrimSpace(state.BaseAsset) == "" ||
		strings.TrimSpace(state.BaselineBaseTotal) == "" ||
		strings.TrimSpace(state.ClientOrderID) == "" ||
		strings.TrimSpace(state.OrderID) == "" {
		return errors.New("smoke state is incomplete")
	}
	if _, err := shared.ParseDecimal(state.BaselineBaseTotal); err != nil {
		return errors.New("smoke state baseline is not a decimal")
	}
	return nil
}

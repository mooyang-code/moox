package service

import "github.com/mooyang-code/moox/modules/trade/internal/exchange"

// Service 是 Trade 模块的服务聚合入口。
// Trade 模块统一承载账户（account）与订单（order）两条交易链路，
// 内部按域拆分为 AccountService 与 OrderService，共享同一 Store 与交易所适配层。
type Service struct {
	module  string
	store   Store
	exNew   ExchangeFactory
	Account *AccountService
	Order   *OrderService
}

// ExchangeFactory 按交易所名创建适配器，默认使用 exchange.New。
type ExchangeFactory func(name string) (exchange.ExchangeAdapter, error)

// Option 配置 Service。
type Option func(*Service)

// WithStore 注入持久化实现。
func WithStore(s Store) Option { return func(svc *Service) { svc.store = s } }

// WithExchangeFactory 注入交易所适配器工厂（便于测试注入 mock）。
func WithExchangeFactory(f ExchangeFactory) Option {
	return func(svc *Service) { svc.exNew = f }
}

// New 创建 Trade 服务聚合。
func New(module string, opts ...Option) *Service {
	if module == "" {
		module = "trade"
	}
	svc := &Service{module: module, exNew: exchange.New}
	for _, opt := range opts {
		opt(svc)
	}
	svc.Account = &AccountService{store: svc.store}
	svc.Order = &OrderService{store: svc.store, exNew: svc.exNew}
	return svc
}

// Health 表示 Trade 模块的健康检查处理器。
type Health struct {
	Module string `json:"module"`
	Ready  bool   `json:"ready"`
}

// Health 返回健康状态。
func (s *Service) Health() Health {
	return Health{Module: s.module, Ready: true}
}

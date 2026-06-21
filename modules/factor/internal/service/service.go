package service

// Service 表示 Factor 模块的服务实现。
type Service struct {
	module string
}

// Health 表示 Factor 模块的健康检查处理器。
type Health struct {
	Module string `json:"module"`
	Ready  bool   `json:"ready"`
}

func New(module string) *Service {
	if module == "" {
		module = "factor"
	}
	return &Service{module: module}
}

func (s *Service) Health() Health {
	return Health{Module: s.module, Ready: true}
}

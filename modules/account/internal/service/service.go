package service

type Service struct {
	module string
}

type Health struct {
	Module string `json:"module"`
	Ready  bool   `json:"ready"`
}

func New(module string) *Service {
	if module == "" {
		module = "account"
	}
	return &Service{module: module}
}

func (s *Service) Health() Health {
	return Health{Module: s.module, Ready: true}
}

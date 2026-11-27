package execution

import "errors"

// InfrastructureError 标记来自共享存储等基础设施故障的错误，用于和单账户执行故障区分。
type InfrastructureError struct{ Err error }

func (e InfrastructureError) Error() string        { return e.Err.Error() }
func (e InfrastructureError) Unwrap() error        { return e.Err }
func (e InfrastructureError) infrastructureFault() {}

// IsInfrastructureError 报告错误链中是否包含基础设施故障。
func IsInfrastructureError(err error) bool {
	var infrastructure interface{ infrastructureFault() }
	return errors.As(err, &infrastructure)
}

// ErrOutOfSession 表示下单时刻处于场所交易时段之外。
var ErrOutOfSession = errors.New(ReasonOutOfSession)

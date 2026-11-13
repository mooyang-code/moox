// Package storeerr 定义存储层错误的类别。存储层（元数据库、Pebble、校验）返回带类别的错误，
// RPC 层只在 retinfo.Code 一处把类别映射成错误码，不按错误文字推断。
package storeerr

import (
	"database/sql"
	"errors"
	"fmt"
)

// Kind 是错误类别；没有标类别的错误一律按内部错误处理。
type Kind uint8

const (
	// Internal 是内部错误：存储故障、缓存未发布等，调用方可以稍后重试。
	Internal Kind = iota
	// Invalid 是参数错误：请求本身不合法，原样重试不会成功。
	Invalid
	// NotFound 表示要操作的对象不存在。
	NotFound
	// Conflict 表示与已有状态冲突：已存在、修订号不符、内容不一致等。
	Conflict
)

// Kinded 由自带类别的错误类型实现（例如 Pebble 存储层的校验与冲突错误）。
type Kinded interface {
	StoreErrorKind() Kind
}

type kindError struct {
	kind Kind
	err  error
}

func (e *kindError) Error() string        { return e.err.Error() }
func (e *kindError) Unwrap() error        { return e.err }
func (e *kindError) StoreErrorKind() Kind { return e.kind }

// New 返回带类别的错误，可以用作哨兵错误（errors.Is 按同一个值匹配）。
func New(kind Kind, message string) error {
	return &kindError{kind: kind, err: errors.New(message)}
}

// Errorf 按 fmt.Errorf 的规则组装错误（支持 %w）并标上类别。
func Errorf(kind Kind, format string, args ...any) error {
	return &kindError{kind: kind, err: fmt.Errorf(format, args...)}
}

// Invalidf、NotFoundf、Conflictf 是 Errorf 的简写。
func Invalidf(format string, args ...any) error  { return Errorf(Invalid, format, args...) }
func NotFoundf(format string, args ...any) error { return Errorf(NotFound, format, args...) }
func Conflictf(format string, args ...any) error { return Errorf(Conflict, format, args...) }

// KindOf 返回错误链上最外层标注的类别；元数据库的“未找到”哨兵算作不存在，其余没有类别的错误算内部错误。
// Pebble 的“未找到”在 datanode 内部各自处理，不会到这里：只有 datanode 认识 Pebble。
func KindOf(err error) Kind {
	var kinded Kinded
	if errors.As(err, &kinded) {
		return kinded.StoreErrorKind()
	}
	if errors.Is(err, sql.ErrNoRows) {
		return NotFound
	}
	return Internal
}

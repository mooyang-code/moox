package common

// 软删除标记常量（对应列 c_is_deleted，字符串型）。
// 'false'=有效，'true'=已删除。
const (
	IsDeletedTrue  = "true"
	IsDeletedFalse = "false"
)

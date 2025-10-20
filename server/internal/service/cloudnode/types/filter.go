package types

// Filter 通用过滤器接口
type Filter interface {
	GetPage() int
	GetPageSize() int
	SetDefaults()
}

// BaseFilter 基础过滤器
type BaseFilter struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

func (f *BaseFilter) GetPage() int {
	if f.Page <= 0 {
		return 1
	}
	return f.Page
}

func (f *BaseFilter) GetPageSize() int {
	if f.PageSize <= 0 {
		return 20
	}
	if f.PageSize > 100 {
		return 100
	}
	return f.PageSize
}

func (f *BaseFilter) SetDefaults() {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = 20
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
}

// SetDefaults 设置过滤器默认值
func (f *NodeFilter) SetDefaults() {
	baseFilter := BaseFilter{Page: f.Page, PageSize: f.PageSize}
	baseFilter.SetDefaults()
	f.Page = baseFilter.Page
	f.PageSize = baseFilter.PageSize
}

// SetDefaults 设置过滤器默认值
func (f *ProbeLogFilter) SetDefaults() {
	baseFilter := BaseFilter{Page: f.Page, PageSize: f.PageSize}
	baseFilter.SetDefaults()
	f.Page = baseFilter.Page
	f.PageSize = baseFilter.PageSize
}

// BaseFilter embedding in existing filters
type NodeFilterBase struct {
	BaseFilter
}

type ProbeLogFilterBase struct {
	BaseFilter
}
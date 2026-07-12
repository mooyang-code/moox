package store

const (
	defaultPageSize = 50
	maxPageSize     = 1000
)

// Page describes one paginated query request.
type Page struct {
	Page     int
	PageSize int
}

func normalizePage(page Page) (int, int) {
	if page.Page <= 0 {
		page.Page = 1
	}
	if page.PageSize <= 0 {
		page.PageSize = defaultPageSize
	}
	if page.PageSize > maxPageSize {
		page.PageSize = maxPageSize
	}
	return page.Page, page.PageSize
}

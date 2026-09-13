package inputcache

import (
	"database/sql/driver"
	"reflect"
	"strconv"
)

// Native JSON scanning decodes numbers through float64 in the DuckDB driver.
// Reading the stored text preserves both numeric precision and JSON null.
func cacheReadColumn(column Column) string {
	name := quoteIdentifier(column.Name)
	if column.Type == "JSON" {
		return "CAST(" + name + " AS VARCHAR) AS " + name
	}
	return name
}

// database/sql cannot bind high-bit uint64 values. Decimal strings let DuckDB
// convert into UBIGINT without a lossy intermediate float or signed integer.
func cacheParameter(columnType string, value any) any {
	if columnType != "UBIGINT" || value == nil {
		return value
	}
	rv := reflect.ValueOf(value)
	for {
		if _, ok := rv.Interface().(driver.Valuer); ok {
			return value
		}
		if rv.Kind() != reflect.Pointer {
			break
		}
		if rv.IsNil() {
			return value
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10)
	default:
		return value
	}
}

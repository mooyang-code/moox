package storage

// AuthInfo carries caller identity for storage requests.
type AuthInfo struct {
	AppID     string `json:"app_id,omitempty"`
	AppKey    string `json:"app_key,omitempty"`
	Operator  string `json:"operator,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// RetInfo is the common storage response status.
type RetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// TypedValue is the JSON representation of storage common.TypedValue.
type TypedValue struct {
	StringValue *string  `json:"string_value,omitempty"`
	IntValue    *int64   `json:"int_value,omitempty"`
	DoubleValue *float64 `json:"double_value,omitempty"`
	JSONValue   *string  `json:"json_value,omitempty"`
}

// ColumnValue represents one typed column in a row.
type ColumnValue struct {
	ColumnName string     `json:"column_name"`
	ValueType  string     `json:"value_type"`
	Value      TypedValue `json:"value"`
}

// Subject is storage metadata for a data subject.
type Subject struct {
	SpaceID     string            `json:"space_id,omitempty"`
	SubjectID   string            `json:"subject_id"`
	SubjectType string            `json:"subject_type"`
	Name        string            `json:"name,omitempty"`
	Market      string            `json:"market,omitempty"`
	Currency    string            `json:"currency,omitempty"`
	Timezone    string            `json:"timezone,omitempty"`
	Status      string            `json:"status,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// DatasetSubject binds a dataset to a subject.
type DatasetSubject struct {
	SpaceID            string            `json:"space_id,omitempty"`
	DatasetID          string            `json:"dataset_id"`
	SubjectID          string            `json:"subject_id,omitempty"`
	SubjectRole        string            `json:"subject_role,omitempty"`
	EffectiveStartTime string            `json:"effective_start_time,omitempty"`
	EffectiveEndTime   string            `json:"effective_end_time,omitempty"`
	Status             string            `json:"status,omitempty"`
	Attributes         map[string]string `json:"attributes,omitempty"`
}

// RegisterDataSubjectRequest registers subject metadata, external symbol and bindings.
type RegisterDataSubjectRequest struct {
	AuthInfo        AuthInfo         `json:"auth_info,omitempty"`
	SpaceID         string           `json:"space_id"`
	DataSourceID    string           `json:"data_source_id"`
	ExternalSymbol  string           `json:"external_symbol"`
	Subject         Subject          `json:"subject"`
	DatasetBindings []DatasetSubject `json:"dataset_bindings,omitempty"`
}

// TimeSeriesKey identifies a time-series row.
type TimeSeriesKey struct {
	SpaceID    string            `json:"space_id"`
	DatasetID  string            `json:"dataset_id"`
	SubjectID  string            `json:"subject_id"`
	Freq       string            `json:"freq"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
	DataTime   string            `json:"data_time"`
}

// TimeSeriesRow is a storage time-series row.
type TimeSeriesRow struct {
	Key        TimeSeriesKey     `json:"key"`
	Columns    []ColumnValue     `json:"columns"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// WriteTimeSeriesRowsRequest writes time-series rows.
type WriteTimeSeriesRowsRequest struct {
	AuthInfo AuthInfo        `json:"auth_info,omitempty"`
	Rows     []TimeSeriesRow `json:"rows"`
}

// RecordKey identifies a record row.
type RecordKey struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	RecordID  string `json:"record_id"`
	Version   string `json:"version,omitempty"`
}

// RecordRow is a storage record row.
type RecordRow struct {
	Key        RecordKey         `json:"key"`
	Columns    []ColumnValue     `json:"columns"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// WriteRecordRowsRequest writes record rows.
type WriteRecordRowsRequest struct {
	AuthInfo AuthInfo    `json:"auth_info,omitempty"`
	Rows     []RecordRow `json:"rows"`
}

func StringField(name, value string) ColumnValue {
	return ColumnValue{ColumnName: name, ValueType: "FIELD_VALUE_TYPE_STRING", Value: TypedValue{StringValue: &value}}
}

func IntField(name string, value int64) ColumnValue {
	return ColumnValue{ColumnName: name, ValueType: "FIELD_VALUE_TYPE_INT", Value: TypedValue{IntValue: &value}}
}

func DoubleField(name string, value float64) ColumnValue {
	return ColumnValue{ColumnName: name, ValueType: "FIELD_VALUE_TYPE_DOUBLE", Value: TypedValue{DoubleValue: &value}}
}

func JSONField(name, value string) ColumnValue {
	return ColumnValue{ColumnName: name, ValueType: "FIELD_VALUE_TYPE_JSON", Value: TypedValue{JSONValue: &value}}
}

package storagepb

import commonpb "github.com/mooyang-code/moox/packages/commonpb"

type AuthInfo = commonpb.AuthInfo
type RetInfo = commonpb.RetInfo
type Page = commonpb.Page
type PageResult = commonpb.PageResult
type TotalMode = commonpb.TotalMode
type TotalState = commonpb.TotalState
type ErrorCode = commonpb.ErrorCode

const (
	TotalMode_AUTO        = commonpb.TotalMode_AUTO
	TotalMode_NONE        = commonpb.TotalMode_NONE
	TotalMode_FORCE_EXACT = commonpb.TotalMode_FORCE_EXACT

	TotalState_UNKNOWN = commonpb.TotalState_UNKNOWN
	TotalState_EXACT   = commonpb.TotalState_EXACT
	TotalState_SKIPPED = commonpb.TotalState_SKIPPED

	ErrorCode_SUCCESS                        = commonpb.ErrorCode_SUCCESS
	ErrorCode_INVALID_PARAM                  = commonpb.ErrorCode_INVALID_PARAM
	ErrorCode_NO_AUTH                        = commonpb.ErrorCode_NO_AUTH
	ErrorCode_NO_PERMISSION                  = commonpb.ErrorCode_NO_PERMISSION
	ErrorCode_INNER_ERR                      = commonpb.ErrorCode_INNER_ERR
	ErrorCode_NOT_FOUND                      = commonpb.ErrorCode_NOT_FOUND
	ErrorCode_SPACE_NOT_FOUND                = commonpb.ErrorCode_SPACE_NOT_FOUND
	ErrorCode_DATASET_NOT_FOUND              = commonpb.ErrorCode_DATASET_NOT_FOUND
	ErrorCode_SUBJECT_NOT_FOUND              = commonpb.ErrorCode_SUBJECT_NOT_FOUND
	ErrorCode_FIELD_NOT_FOUND                = commonpb.ErrorCode_FIELD_NOT_FOUND
	ErrorCode_FACTOR_NOT_FOUND               = commonpb.ErrorCode_FACTOR_NOT_FOUND
	ErrorCode_VIEW_NOT_FOUND                 = commonpb.ErrorCode_VIEW_NOT_FOUND
	ErrorCode_VIEW_NOT_READY                 = commonpb.ErrorCode_VIEW_NOT_READY
	ErrorCode_VIEW_COLUMN_NOT_FOUND          = commonpb.ErrorCode_VIEW_COLUMN_NOT_FOUND
	ErrorCode_QUERY_SHAPE_UNSUPPORTED        = commonpb.ErrorCode_QUERY_SHAPE_UNSUPPORTED
	ErrorCode_ROUTE_NOT_FOUND                = commonpb.ErrorCode_ROUTE_NOT_FOUND
	ErrorCode_ROUTE_CROSS_DEVICE_UNSUPPORTED = commonpb.ErrorCode_ROUTE_CROSS_DEVICE_UNSUPPORTED
	ErrorCode_ENGINE_CAPABILITY_UNSUPPORTED  = commonpb.ErrorCode_ENGINE_CAPABILITY_UNSUPPORTED
	ErrorCode_DIMENSION_VALUE_INVALID        = commonpb.ErrorCode_DIMENSION_VALUE_INVALID
	ErrorCode_SUBJECT_NOT_IN_DATASET         = commonpb.ErrorCode_SUBJECT_NOT_IN_DATASET
	ErrorCode_REVISION_CONFLICT              = commonpb.ErrorCode_REVISION_CONFLICT
)

package retinfo

import (
	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func Success(msg string) *pb.RetInfo {
	if msg == "" {
		msg = "success"
	}
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: msg}
}

func Error(code pb.ErrorCode, err error) *pb.RetInfo {
	if err == nil {
		return &pb.RetInfo{Code: code}
	}
	return &pb.RetInfo{Code: code, Msg: err.Error()}
}

// Code 把存储层错误按类别映射成错误码，是全模块唯一的映射处：参数错误、不存在、冲突各有错误码，其余都是内部错误。
// Factor 只把 INNER_ERR 当作基础设施错误重试，所以没有标类别的错误一律落到 INNER_ERR。
func Code(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	switch storeerr.KindOf(err) {
	case storeerr.Invalid:
		return pb.ErrorCode_INVALID_PARAM
	case storeerr.NotFound:
		return pb.ErrorCode_NOT_FOUND
	case storeerr.Conflict:
		return pb.ErrorCode_CONFLICT
	default:
		return pb.ErrorCode_INNER_ERR
	}
}

// FromError 返回存储层错误对应的返回信息。
func FromError(err error) *pb.RetInfo {
	return Error(Code(err), err)
}

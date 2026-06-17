package response

import pb "github.com/mooyang-code/moox/modules/storage/proto/gen"

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

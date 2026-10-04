//go:build !cgo

package view

import pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"

func successRetInfo() *pb.RetInfo { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"} }

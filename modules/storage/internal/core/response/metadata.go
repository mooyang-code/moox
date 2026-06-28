package response

import (
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// MetadataStoreCode maps metadata store errors to RPC error codes.
// Validation-style messages stay INVALID_PARAM; persistence failures become INNER_ERR.
//
// NOTE: mapping relies on English error message substrings (e.g. " is required", " must ").
// If validation messages change locale or wording, update this function or switch to typed errors.
func MetadataStoreCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, " is required"),
		strings.Contains(msg, "invalid "),
		strings.Contains(msg, " must "),
		strings.Contains(msg, "unsupported "),
		strings.Contains(msg, "does not support"):
		return pb.ErrorCode_INVALID_PARAM
	default:
		return pb.ErrorCode_INNER_ERR
	}
}

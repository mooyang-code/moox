package response

import (
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
)

func TestSuccessUsesDefaultMessage(t *testing.T) {
	got := Success("")
	assert.Equal(t, pb.ErrorCode_SUCCESS, got.GetCode())
	assert.Equal(t, "success", got.GetMsg())
}

func TestErrorIncludesMessageWhenPresent(t *testing.T) {
	got := Error(pb.ErrorCode_INVALID_PARAM, errors.New("bad input"))
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, got.GetCode())
	assert.Equal(t, "bad input", got.GetMsg())

	empty := Error(pb.ErrorCode_INNER_ERR, nil)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, empty.GetCode())
	assert.Empty(t, empty.GetMsg())
}

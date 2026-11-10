package operator

import (
	"errors"
	"testing"
)

// 从库里还原的失败要能被识别并保留原文，接口层据此回原因；有错误码的仍还原成对应的类别错误。
func TestRecordedFailureKeepsMessage(t *testing.T) {
	err := manualPersistedError("某个没有错误码的失败", "")
	if !errors.Is(err, ErrRecordedFailure) || err.Error() != "某个没有错误码的失败" {
		t.Fatalf("err = %v", err)
	}
	coded := manualPersistedError("参数不合法：数量", "INVALID_COMMAND")
	if !errors.Is(coded, ErrInvalidCommand) || coded.Error() != "参数不合法：数量" {
		t.Fatalf("coded = %v", coded)
	}
}

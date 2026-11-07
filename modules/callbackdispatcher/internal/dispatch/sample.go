package dispatch

import (
	"encoding/json"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/catalog"
)

// SampleJSON 返回事件目录里该事件的示例内容（JSON），“发一条测试”用它充当事件的 data。
func SampleJSON(entry catalog.Entry) (string, error) {
	raw, err := json.Marshal(entry.Sample)
	return string(raw), err
}

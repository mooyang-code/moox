package primarystore

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func rejectUnauthorizedUpsertSemantics(_ []*pb.RowFieldUpsert, writeSource string) error {
	if privilegedWriteSource(writeSource) {
		return fmt.Errorf("write_source %q cannot grant factor write privileges", writeSource)
	}
	return nil
}

func privilegedWriteSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "factor", "moox-factor", pebble.WriteKindFactorResult:
		return true
	default:
		return false
	}
}

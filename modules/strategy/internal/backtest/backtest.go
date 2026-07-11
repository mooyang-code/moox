package backtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type Job struct {
	ID, ConfigHash string
	Decisions      []domain.Output
}

func HashDecision(o domain.Output) string {
	b, _ := json.Marshal(o)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

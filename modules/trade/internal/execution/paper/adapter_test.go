package paper

import (
	"crypto/sha256"
	"encoding/hex"
)

func PaperIDs(account, client string) (string, string, string) {
	sum := sha256.Sum256([]byte(account + "\x00" + client))
	suffix := hex.EncodeToString(sum[:12])
	return "paper-order-" + suffix, "paper-trade-" + suffix, "paper-fill-" + suffix
}

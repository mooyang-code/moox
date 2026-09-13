package gatewayauth

import (
	"testing"
	"time"
)

func TestValidateTargetNodeMatchesSigning(t *testing.T) {
	for _, node := range []string{"storage-1", "node/1", "", " ", " node", "node\n", string([]byte{0xff})} {
		err := ValidateTargetNode(node)
		_, signingErr := Sign(Credentials{KeyID: "test-key", Secret: "test-secret"}, Request{Method: "POST", Path: "/test", TargetNode: node}, time.Now())
		if (err == nil) != (signingErr == nil) {
			t.Fatalf("target validation and signing differ for %q: %v / %v", node, err, signingErr)
		}
	}
}

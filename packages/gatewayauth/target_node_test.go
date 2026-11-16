package gatewayauth

import (
	"testing"
	"time"
)

func TestValidateTargetNodeMatchesSigning(t *testing.T) {
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"storage-1", "node/1", "", " ", " node", "node\n", "a b", string([]byte{0xff})} {
		err := ValidateTargetNode(node)
		_, signingErr := Sign(Credentials{Caller: "moox-cli", Key: key}, Request{TargetNode: node}, time.Now())
		if (err == nil) != (signingErr == nil) {
			t.Fatalf("target validation and signing differ for %q: %v / %v", node, err, signingErr)
		}
	}
}

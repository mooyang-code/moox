package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
)

func main() {
	body := []byte(os.Getenv("MOOX_PROBE_BODY"))
	if len(body) == 0 {
		body = []byte(`{"filter":{"space_id":"crypto","page":{"page":1,"page_size":200}}}`)
	}
	target := os.Getenv("MOOX_PROBE_TARGET")
	secret := os.Getenv("MOOX_PROBE_SECRET")
	base := os.Getenv("MOOX_PROBE_URL")
	service := getenvDefault("MOOX_PROBE_SERVICE", "collectmgr")
	method := getenvDefault("MOOX_PROBE_METHOD", "GetTaskInstanceList")
	path := "/api/service/" + service + "/" + method
	header, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: "moox-cli", Caller: "moox-cli", Secret: secret}, gatewayauth.Request{
		Method: http.MethodPost, Path: path, TargetNode: target, Caller: "moox-cli", Body: body,
	}, time.Now())
	if err != nil { panic(err) }
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil { panic(err) }
	req.Header.Set("Content-Type", "application/json")
	if spaceID := os.Getenv("MOOX_PROBE_SPACE_ID"); spaceID != "" {
		req.Header.Set("X-Space-Id", spaceID)
	}
	for key, values := range header { for _, value := range values { req.Header.Add(key, value) } }
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil { panic(err) }
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	fmt.Printf("status=%s\n%s\n", resp.Status, raw)
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" { return value }
	return fallback
}

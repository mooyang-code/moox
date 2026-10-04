package tencent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func tc3Do(ctx context.Context, httpClient *http.Client, secretID, secretKey, region, service, version, action, endpoint string, now func() time.Time, payload, out any) error {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	timestamp := now().Unix()
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	hashedBody := sha256HexCVM(body)
	canonical := strings.Join([]string{
		"POST", "/", "", "content-type:application/json\nhost:" + parsed.Host + "\n", "content-type;host", hashedBody,
	}, "\n")
	scope := fmt.Sprintf("%s/%s/tc3_request", date, service)
	stringToSign := strings.Join([]string{"TC3-HMAC-SHA256", strconv.FormatInt(timestamp, 10), scope, sha256HexCVM([]byte(canonical))}, "\n")
	key := hmacCVM([]byte("TC3"+secretKey), date)
	key = hmacCVM(key, service)
	key = hmacCVM(key, "tc3_request")
	signature := hex.EncodeToString(hmacCVM(key, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=content-type;host, Signature=%s", secretID, scope, signature))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", parsed.Host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-TC-Version", version)
	if strings.TrimSpace(region) != "" {
		req.Header.Set("X-TC-Region", region)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s failed: %w", action, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s response: %w", action, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request %s returned HTTP %s: %s", action, resp.Status, string(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", action, err)
	}
	return nil
}

func apiCodeMessage(err *apiError) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %s", err.Code, err.Message)
}

func isAlreadyDoneAPIError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, token := range []string{
		"already",
		"alreadyattached",
		"ccnattached",
		"ccnalreadyattached",
		"ccnnotattached",
		"not attached",
		"resourceinuse",
		"alreadyexists",
		"已关联",
		"未关联",
	} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

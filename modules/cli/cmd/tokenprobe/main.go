package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/security"
)

func main() {
	baseURL := strings.TrimRight(os.Getenv("MOOX_CONTROL_URL"), "/")
	username := os.Getenv("MOOX_ADMIN_USERNAME")
	password := os.Getenv("MOOX_ADMIN_PASSWORD")
	if baseURL == "" || username == "" || password == "" {
		panic("missing login environment")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // internal one-off probe
	saltRaw := post(client, baseURL+"/api/admin/auth/GetLoginSalt", map[string]any{"username": username})
	var salt struct {
		RetInfo struct{ Code int `json:"code"` } `json:"ret_info"`
		Salt string `json:"salt"`
		Timestamp string `json:"timestamp"`
	}
	decode(saltRaw, &salt)
	timestamp, err := strconv.ParseInt(salt.Timestamp, 10, 64)
	if salt.RetInfo.Code != 0 || salt.Salt == "" || timestamp <= 0 {
		panic("login salt rejected")
	}
	encrypted, err := security.Encrypt(password, salt.Salt+strconv.FormatInt(timestamp, 10))
	if err != nil { panic(err) }
	loginRaw := post(client, baseURL+"/api/admin/auth/Login", map[string]any{
		"username": username, "password_hash": encrypted, "salt": salt.Salt,
		"timestamp": timestamp, "device_id": "moox-cli-namespace-delete",
		"user_agent": "moox-cli/namespace-delete", "client_ip": "127.0.0.1",
	})
	var login struct {
		RetInfo struct{ Code int `json:"code"`; Msg string `json:"msg"` } `json:"ret_info"`
		AccessToken string `json:"access_token"`
	}
	decode(loginRaw, &login)
	if login.RetInfo.Code != 0 || login.AccessToken == "" { panic("login rejected: "+login.RetInfo.Msg) }
	fmt.Print(login.AccessToken)
}

func post(client *http.Client, endpoint string, body any) []byte {
	raw, err := json.Marshal(body); if err != nil { panic(err) }
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(raw)); if err != nil { panic(err) }
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body); if err != nil { panic(err) }
	if resp.StatusCode != http.StatusOK { panic(fmt.Sprintf("http %s: %s", resp.Status, string(data))) }
	return data
}

func decode(raw []byte, dst any) { if err := json.Unmarshal(raw, dst); err != nil { panic(err) } }

// Package consoleclient 以管理员身份调用管理台的 HTTPS 接口（/api/admin/<服务>/<方法>）。
//
// 用于换电脑恢复配置这类「本机还没有 moox.toml、也没有操作员网关身份」的场景：
// 和浏览器一样先取一次性盐值、加密密码登录，之后每个请求用登录下发的签名密钥签名。
package consoleclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

// DefaultPort 是管理台 HTTPS 入口（console-proxy）的端口。
const DefaultPort = 9527

// UntrustedError 表示管理台的证书不被这台电脑信任（例如用的是 MooX 内部 CA）；
// 用户核对指纹无误后，用 Options.Fingerprint 固定这张证书再连。
type UntrustedError struct {
	Fingerprint string
}

func (e *UntrustedError) Error() string {
	return "这台电脑不信任管理台的 HTTPS 证书，指纹 " + e.Fingerprint
}

// Options 是连接参数。
type Options struct {
	// Address 是管理台地址：IP、域名，或带端口的 host:port；不带端口时用 9527。
	Address string
	// Fingerprint 不为空时只接受 SHA-256 指纹等于它的证书（用户确认过的自签证书）。
	Fingerprint string
	// RootCAs 不为空时用它校验证书链（例如部署时缓存的管理台内部 CA 根证书），否则用系统信任。
	RootCAs *x509.CertPool
	// HTTPClient 为空时按上面的规则建一个；测试里替换。
	HTTPClient *http.Client
}

// Client 是登录后的管理台会话。
type Client struct {
	base       string
	http       *http.Client
	token      string
	signingKey string
}

// New 校验地址并准备 HTTPS 客户端，还没有登录。
func New(options Options) (*Client, error) {
	base, err := baseURL(options.Address)
	if err != nil {
		return nil, err
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			Proxy: nil, TLSClientConfig: tlsConfig(strings.ToLower(strings.TrimSpace(options.Fingerprint)), options.RootCAs), ForceAttemptHTTP2: true,
		}}
	}
	return &Client{base: base, http: client}, nil
}

func baseURL(address string) (string, error) {
	address = strings.TrimSpace(address)
	address = strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://")
	address = strings.TrimSuffix(address, "/")
	if address == "" || strings.ContainsAny(address, "/?#@ ") {
		return "", errors.New("请填写管理台的 IP 或域名，例如 106.53.10.20")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host, port = address, strconv.Itoa(DefaultPort)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || host == "" {
		return "", errors.New("管理台地址不对")
	}
	return "https://" + net.JoinHostPort(host, port), nil
}

// tlsConfig 默认按系统信任校验证书；给了指纹时改为只认这张证书（自签或内部 CA 的情况）。
func tlsConfig(fingerprint string, roots *x509.CertPool) *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if fingerprint == "" {
		return config
	}
	config.InsecureSkipVerify = true // 下面用固定指纹代替证书链校验
	config.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 || Fingerprint(raw[0]) != fingerprint {
			return errors.New("管理台的证书和确认过的指纹不一致")
		}
		return nil
	}
	return config
}

// Fingerprint 是证书的 SHA-256 指纹（小写十六进制，冒号分隔）。
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = hex.EncodeToString([]byte{b})
	}
	return strings.Join(parts, ":")
}

// Login 用管理员账号登录：先取一次性盐值，用它加密密码后登录，记下访问令牌与签名密钥。
func (c *Client) Login(ctx context.Context, username, password string) error {
	var salt struct {
		RetInfo   retInfo   `json:"ret_info"`
		Salt      string    `json:"salt"`
		Timestamp flexInt64 `json:"timestamp"`
	}
	if err := c.post(ctx, "auth", "GetLoginSalt", map[string]string{"username": username}, false, &salt); err != nil {
		return err
	}
	if err := salt.RetInfo.err("获取登录盐值"); err != nil {
		return err
	}
	encrypted, err := mooxsecurity.Encrypt(password, salt.Salt+strconv.FormatInt(int64(salt.Timestamp), 10))
	if err != nil {
		return err
	}
	var login struct {
		RetInfo           retInfo `json:"ret_info"`
		AccessToken       string  `json:"access_token"`
		RequestSigningKey string  `json:"request_signing_key"`
	}
	request := map[string]any{
		"username": username, "password_hash": encrypted, "salt": salt.Salt, "timestamp": int64(salt.Timestamp),
		"device_id": "moox-cli", "user_agent": "moox-cli",
	}
	if err := c.post(ctx, "auth", "Login", request, false, &login); err != nil {
		return err
	}
	if err := login.RetInfo.err("登录管理台"); err != nil {
		return err
	}
	if login.AccessToken == "" || login.RequestSigningKey == "" {
		return errors.New("登录管理台失败：响应里没有会话信息")
	}
	c.token, c.signingKey = login.AccessToken, login.RequestSigningKey
	return nil
}

// Call 以登录后的身份调用 /api/admin/<service>/<method>，并检查 ret_info。
func (c *Client) Call(ctx context.Context, service, method string, request, response any) error {
	if c.token == "" {
		return errors.New("还没有登录管理台")
	}
	return c.post(ctx, service, method, request, true, response)
}

func (c *Client) post(ctx context.Context, service, method string, request any, signed bool, response any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	path := "/api/admin/" + service + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if signed {
		timestamp := time.Now().Unix()
		nonce, err := requestauth.NewNonce()
		if err != nil {
			return err
		}
		signature, err := requestauth.Sign(c.signingKey, requestauth.Material{Method: http.MethodPost, Path: path, Body: body, Timestamp: timestamp, Nonce: nonce})
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.token)
		req.Header.Set("X-Access-Token", c.token)
		req.Header.Set("X-Moox-Timestamp", strconv.FormatInt(timestamp, 10))
		req.Header.Set("X-Moox-Nonce", nonce)
		req.Header.Set("X-Moox-Signature", signature)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return describeTransportError(err, c.base)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("读取管理台响应失败：%w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("管理台拒绝了这次请求：登录已失效，请重新登录")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("管理台返回 HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return fmt.Errorf("管理台的响应格式不对：%w", err)
	}
	return nil
}

// describeTransportError 把证书不受信任单独区分出来，交给调用方让用户核对指纹。
func describeTransportError(err error, base string) error {
	// macOS 等平台用系统校验，不受信任时返回的不是 x509 的具体类型；统一从握手校验错误里取对方证书。
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) && len(verification.UnverifiedCertificates) > 0 {
		return &UntrustedError{Fingerprint: Fingerprint(verification.UnverifiedCertificates[0].Raw)}
	}
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) && unknown.Cert != nil {
		return &UntrustedError{Fingerprint: Fingerprint(unknown.Cert.Raw)}
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) && hostname.Certificate != nil {
		return &UntrustedError{Fingerprint: Fingerprint(hostname.Certificate.Raw)}
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) && invalid.Cert != nil {
		return &UntrustedError{Fingerprint: Fingerprint(invalid.Cert.Raw)}
	}
	return fmt.Errorf("连不上管理台 %s，请确认地址正确、管理台正在运行，并且这台电脑能访问它", strings.TrimPrefix(base, "https://"))
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (r retInfo) err(action string) error {
	if r.Code == 0 {
		return nil
	}
	if strings.TrimSpace(r.Msg) == "" {
		return fmt.Errorf("%s失败（错误码 %d）", action, r.Code)
	}
	return fmt.Errorf("%s失败：%s", action, r.Msg)
}

// RetError 检查业务接口返回的 ret_info，供调用方使用。
func RetError(code int, msg, action string) error {
	return retInfo{Code: code, Msg: msg}.err(action)
}

// flexInt64 同时接受 JSON 数字和字符串形式的 int64（protojson 会把 int64 编成字符串）。
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*f = 0
		return nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*f = flexInt64(value)
	return nil
}

// Int64 是可同时接受数字和字符串的 int64，供调用方解码响应。
type Int64 = flexInt64

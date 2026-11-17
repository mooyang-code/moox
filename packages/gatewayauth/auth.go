// Package gatewayauth 是 MooX 调用方身份的签名与校验：每台主机一把 ed25519 私钥，这台主机上的组件共用；
// 不属于主机的身份（moox-cli、scf-collector、moox-skill、collector-operator）各一把。校验方只持有公钥。
package gatewayauth

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

// Version 是签名材料的第一行；改签名材料就换版本号。
const Version = "moox-gateway-auth-v2"

// RetRejected 是网关（与外部入口）自己拒签时返回的框架错误码：验签、时间窗、防重放、调用名单任何一步不过。
// 验签在转发之前，收到它说明请求没有到达服务；客户端只对这个错误码重新签名重发一次，写方法也一样。
// 服务自己返回的鉴权类错误用别的错误码，不会重发。
const RetRejected = 11403

// HeaderVerifiedHost 由网关在验签通过后写给本机被调服务：验过的签名主机 ID（非主机身份为空）。
// 网关在入口剥掉外面带进来的全部 x-moox- 头，所以这个头只可能来自本机网关。
const HeaderVerifiedHost = "X-Moox-Verified-Host"

// HeaderVerifiedCaller 由网关在验签通过后写给本机的 SecretMgr：由主机密钥签名的调用方名字（组件名）；moox-cli 等没有签名主机的身份不写这个头。
// 和 HeaderVerifiedHost 一样只可能来自本机网关。SecretMgr 据此把组件调用方能取的密钥限定在它该用的类别内。
const HeaderVerifiedCaller = "X-Moox-Verified-Caller"

// HeaderOriginCaller 由入口主机的网关在把外部身份的请求转给别的主机时带上：值是验过签的外部身份。
// 它不在签名材料里；目标网关只在签名的是入口主机的 host-gateway、值是外部身份且该身份在方法名单上时才认。
const HeaderOriginCaller = "X-Moox-Origin-Caller"

const (
	headerCaller     = "X-Moox-Caller"
	headerHost       = "X-Moox-Host"
	headerTimestamp  = "X-Moox-Timestamp"
	headerNonce      = "X-Moox-Nonce"
	headerTargetNode = "X-Moox-Target-Node"
	headerSignature  = "X-Moox-Signature"

	// Expire 是签名的有效期，ClockSkew 是允许的时钟偏差：签名时间前 ClockSkew 到后 Expire+ClockSkew 之间有效。
	Expire    = 60 * time.Second
	ClockSkew = 30 * time.Second
)

// Credentials 是一个调用方的签名身份。Host 是签名主机 ID，组件必填；不属于主机的身份留空。
type Credentials struct {
	Caller, Host string
	Key          ed25519.PrivateKey
}

// String 与 GoString 不输出私钥。
func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{caller:%s host:%s}", c.Caller, c.Host)
}
func (c Credentials) GoString() string { return c.String() }

// PublicKey 返回私钥对应的公钥；私钥不合法时为 nil。
func (c Credentials) PublicKey() ed25519.PublicKey {
	if len(c.Key) != ed25519.PrivateKeySize {
		return nil
	}
	return c.Key.Public().(ed25519.PublicKey)
}

func (c Credentials) validate() error {
	if !validIdentifier(c.Caller) || c.Host != "" && !validIdentifier(c.Host) {
		return errors.New("签名身份的调用方或主机不合法")
	}
	if len(c.Key) != ed25519.PrivateKeySize {
		return errors.New("签名身份没有 ed25519 私钥")
	}
	return nil
}

// Request 是被签名的一次调用：目标主机、被调服务与方法、请求体。
type Request struct {
	TargetNode   string
	Callee, Func string
	Body         []byte
}

// Claims 是验签通过后的结果。
type Claims struct {
	Caller, Host, Nonce, TargetNode string
	Timestamp                       int64
	// TTL 是这次签名从现在起还有效多久，防重放记录保留这么久。
	TTL time.Duration
}

// Sign 生成 6 个签名头。
func Sign(c Credentials, req Request, now time.Time) (http.Header, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := ValidateTargetNode(req.TargetNode); err != nil {
		return nil, err
	}
	nonce, err := mooxsecurity.RandomHex(32)
	if err != nil {
		return nil, fmt.Errorf("生成 nonce: %w", err)
	}
	timestamp := now.Unix()
	signature := ed25519.Sign(c.Key, material(c.Caller, c.Host, req, timestamp, nonce))
	header := make(http.Header, 6)
	header.Set(headerCaller, c.Caller)
	header.Set(headerHost, c.Host)
	header.Set(headerTimestamp, strconv.FormatInt(timestamp, 10))
	header.Set(headerNonce, nonce)
	header.Set(headerTargetNode, req.TargetNode)
	header.Set(headerSignature, hex.EncodeToString(signature))
	return header, nil
}

// verify 用给定公钥校验签名头与时间窗。调用方与主机由调用者先从头里取出并选好公钥。
func verify(public ed25519.PublicKey, caller, host string, req Request, header http.Header, now time.Time) (Claims, error) {
	timestampValue, err := singleHeader(header, headerTimestamp)
	if err != nil {
		return Claims{}, err
	}
	timestamp, err := strconv.ParseInt(timestampValue, 10, 64)
	if err != nil || timestamp <= 0 {
		return Claims{}, errors.New("签名时间不合法")
	}
	nonce, err := singleHeader(header, headerNonce)
	if err != nil {
		return Claims{}, err
	}
	if !isLowerHex(nonce, 32) {
		return Claims{}, errors.New("nonce 不合法")
	}
	targetNode, err := singleHeader(header, headerTargetNode)
	if err != nil {
		return Claims{}, err
	}
	if targetNode == "" || targetNode != req.TargetNode {
		return Claims{}, errors.New("签名的目标主机不是本机")
	}
	signatureValue, err := singleHeader(header, headerSignature)
	if err != nil {
		return Claims{}, err
	}
	if !isLowerHex(signatureValue, ed25519.SignatureSize) {
		return Claims{}, errors.New("签名格式不合法")
	}
	signature, _ := hex.DecodeString(signatureValue)
	if len(public) != ed25519.PublicKeySize || !ed25519.Verify(public, material(caller, host, req, timestamp, nonce), signature) {
		return Claims{}, errors.New("签名不匹配")
	}
	signedAt := time.Unix(timestamp, 0)
	validUntil := signedAt.Add(Expire).Add(ClockSkew)
	if now.Before(signedAt.Add(-ClockSkew)) || now.After(validUntil) {
		return Claims{}, errors.New("签名已过期或签名时间在未来")
	}
	ttl := validUntil.Sub(now)
	if ttl == 0 {
		ttl = time.Nanosecond
	}
	return Claims{Caller: caller, Host: host, Nonce: nonce, TargetNode: targetNode, Timestamp: timestamp, TTL: ttl}, nil
}

func singleHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", fmt.Errorf("签名头 %s 必须恰好出现一次", name)
	}
	return values[0], nil
}

// optionalHeader 取至多出现一次的头；没有时返回空串（签名主机头对非主机身份为空，有的传输层不带空值的头）。
func optionalHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) > 1 {
		return "", fmt.Errorf("签名头 %s 最多出现一次", name)
	}
	if len(values) == 0 {
		return "", nil
	}
	return values[0], nil
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		!strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

// ValidateTargetNode 用和签名相同的规则检查目标主机，服务可以在打开资源之前就拒绝不可用的配置。
func ValidateTargetNode(value string) error {
	if !validIdentifier(value) {
		return errors.New("gateway target node is invalid")
	}
	return nil
}

// material 是签名材料：各字段不含换行（标识符与十六进制），按行拼接后没有歧义。
func material(caller, host string, req Request, timestamp int64, nonce string) []byte {
	return []byte(strings.Join([]string{
		Version,
		caller,
		host,
		req.Callee,
		req.Func,
		mooxsecurity.SHA256Hex(req.Body),
		strconv.FormatInt(timestamp, 10),
		nonce,
		req.TargetNode,
	}, "\n"))
}

func isLowerHex(value string, byteLen int) bool {
	if len(value) != byteLen*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == byteLen
}

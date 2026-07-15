// Package gatewayauth authenticates requests addressed to a specific MooX node.
package gatewayauth

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	mooxcrypto "github.com/mooyang-code/moox/packages/crypto"
	"github.com/mooyang-code/moox/packages/requestauth"
)

const Version = "moox-gateway-auth-v1"

const (
	headerKeyID      = "X-Moox-Key-Id"
	headerTimestamp  = "X-Moox-Timestamp"
	headerNonce      = "X-Moox-Nonce"
	headerTargetNode = "X-Moox-Target-Node"
	headerSignature  = "X-Moox-Signature"

	defaultExpire    = 60 * time.Second
	defaultClockSkew = 30 * time.Second
)

type Credentials struct {
	KeyID, Secret     string
	Expire, ClockSkew time.Duration
}

type Request struct {
	Method, Path, TargetNode string
	Body                     []byte
}

type Claims struct {
	KeyID, Nonce, TargetNode string
	Timestamp                int64
	TTL                      time.Duration
}

func Sign(c Credentials, req Request, now time.Time) (http.Header, error) {
	_, _, err := validateCredentials(c)
	if err != nil {
		return nil, err
	}
	path, err := escapedPath(req)
	if err != nil {
		return nil, err
	}
	nonce, err := requestauth.NewNonce()
	if err != nil {
		return nil, err
	}
	timestamp := now.Unix()
	signature := sign(c.Secret, req.Method, path, req.Body, timestamp, nonce, req.TargetNode)
	header := make(http.Header, 5)
	header.Set(headerKeyID, c.KeyID)
	header.Set(headerTimestamp, strconv.FormatInt(timestamp, 10))
	header.Set(headerNonce, nonce)
	header.Set(headerTargetNode, req.TargetNode)
	header.Set(headerSignature, signature)
	return header, nil
}

func Verify(c Credentials, req Request, header http.Header, now time.Time) (Claims, error) {
	expire, skew, err := validateCredentials(c)
	if err != nil {
		return Claims{}, err
	}
	path, err := escapedPath(req)
	if err != nil {
		return Claims{}, err
	}
	keyID, err := singleHeader(header, headerKeyID)
	if err != nil {
		return Claims{}, err
	}
	if keyID == "" || keyID != c.KeyID {
		return Claims{}, errors.New("invalid gateway key ID")
	}
	timestampValue, err := singleHeader(header, headerTimestamp)
	if err != nil {
		return Claims{}, err
	}
	timestamp, err := strconv.ParseInt(timestampValue, 10, 64)
	if err != nil || timestamp <= 0 {
		return Claims{}, errors.New("invalid gateway timestamp")
	}
	nonce, err := singleHeader(header, headerNonce)
	if err != nil {
		return Claims{}, err
	}
	if !isLowerHex(nonce, 32) {
		return Claims{}, errors.New("invalid gateway nonce")
	}
	targetNode, err := singleHeader(header, headerTargetNode)
	if err != nil {
		return Claims{}, err
	}
	if targetNode == "" || targetNode != req.TargetNode {
		return Claims{}, errors.New("gateway target node does not match request")
	}
	signature, err := singleHeader(header, headerSignature)
	if err != nil {
		return Claims{}, err
	}
	if !isLowerHex(signature, 32) {
		return Claims{}, errors.New("invalid gateway signature")
	}

	signedAt := time.Unix(timestamp, 0)
	if now.Before(signedAt.Add(-skew)) || now.After(signedAt.Add(expire+skew)) {
		return Claims{}, errors.New("gateway signature expired or timestamp is in the future")
	}
	expected := sign(c.Secret, req.Method, path, req.Body, timestamp, nonce, targetNode)
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return Claims{}, errors.New("gateway signature does not match")
	}
	return Claims{
		KeyID: keyID, Nonce: nonce, TargetNode: targetNode, Timestamp: timestamp,
		TTL: expire + skew,
	}, nil
}

func singleHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", fmt.Errorf("gateway header %s must appear exactly once", name)
	}
	return values[0], nil
}

func validateCredentials(c Credentials) (time.Duration, time.Duration, error) {
	if strings.TrimSpace(c.KeyID) == "" || strings.TrimSpace(c.Secret) == "" {
		return 0, 0, errors.New("gateway key ID and secret are required")
	}
	if strings.ContainsAny(c.KeyID, "\r\n") {
		return 0, 0, errors.New("gateway key ID cannot contain newlines")
	}
	if c.Expire < 0 || c.ClockSkew < 0 {
		return 0, 0, errors.New("gateway expiry and clock skew cannot be negative")
	}
	expire := c.Expire
	if expire == 0 {
		expire = defaultExpire
	}
	skew := c.ClockSkew
	if skew == 0 {
		skew = defaultClockSkew
	}
	return expire, skew, nil
}

func escapedPath(req Request) (string, error) {
	if strings.TrimSpace(req.Method) == "" {
		return "", errors.New("gateway request method is required")
	}
	if strings.TrimSpace(req.TargetNode) == "" || strings.ContainsAny(req.TargetNode, "\r\n") {
		return "", errors.New("gateway target node is invalid")
	}
	if req.Path == "" || !strings.HasPrefix(req.Path, "/") {
		return "", errors.New("gateway request path must be absolute")
	}
	parsed, err := url.ParseRequestURI(req.Path)
	if err != nil || parsed.Host != "" || parsed.Scheme != "" {
		return "", errors.New("gateway request path is invalid")
	}
	path := parsed.EscapedPath()
	if path == "" {
		return "", errors.New("gateway request path is invalid")
	}
	return path, nil
}

func sign(secret, method, path string, body []byte, timestamp int64, nonce, targetNode string) string {
	material := strings.Join([]string{
		Version,
		strings.ToUpper(method),
		path,
		mooxcrypto.SHA256Hex(body),
		strconv.FormatInt(timestamp, 10),
		nonce,
		targetNode,
	}, "\n")
	return mooxcrypto.HMACSHA256Hex(secret, []byte(material))
}

func isLowerHex(value string, byteLen int) bool {
	if len(value) != byteLen*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == byteLen
}

// Package serviceauth implements replay-resistant authentication for /api/service requests.
package serviceauth

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
)

const Version = "moox-auth-v2"

type Config struct {
	AccessKey        string
	SecretKey        string
	ExpireSeconds    int64
	ClockSkewSeconds int64
}

type Request struct {
	Method  string
	Path    string
	Body    []byte
	Headers map[string]string
}

type Claims struct {
	AccessKey string
	Nonce     string
	Timestamp int64
	TTL       time.Duration
}

func BuildHeader(cfg Config, req Request, now time.Time) (string, error) {
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return "", errors.New("service auth access_key and secret_key are required")
	}
	expire := cfg.ExpireSeconds
	if expire <= 0 {
		expire = 60
	}
	nonce, err := requestauth.NewNonce()
	if err != nil {
		return "", err
	}
	material := requestauth.Material{
		Method: req.Method, Path: normalizedPath(req.Path), Body: signingBody(req.Body, expire), Headers: req.Headers,
		Timestamp: now.Unix(), Nonce: nonce,
	}
	signature, err := requestauth.Sign(cfg.SecretKey, material)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%d/%d/%s/%s", Version, cfg.AccessKey, now.Unix(), expire, nonce, signature), nil
}

func VerifyHeader(cfg Config, req Request, header string, now time.Time) (Claims, error) {
	parts := strings.Split(header, "/")
	if len(parts) < 2 {
		return Claims{}, errors.New("invalid auth format")
	}
	if parts[0] != Version {
		return Claims{}, errors.New("invalid auth version")
	}
	if len(parts) != 6 {
		return Claims{}, errors.New("invalid auth format")
	}
	if parts[1] != cfg.AccessKey || cfg.SecretKey == "" {
		return Claims{}, errors.New("invalid service credentials")
	}
	timestamp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Claims{}, errors.New("invalid timestamp")
	}
	expire, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || expire <= 0 || (cfg.ExpireSeconds > 0 && expire > cfg.ExpireSeconds) {
		return Claims{}, errors.New("invalid expire time")
	}
	skew := cfg.ClockSkewSeconds
	if skew <= 0 {
		skew = 30
	}
	if now.Unix()+skew < timestamp || now.Unix() > timestamp+expire+skew {
		return Claims{}, errors.New("auth signature expired or timestamp is in the future")
	}
	material := requestauth.Material{
		Method: req.Method, Path: normalizedPath(req.Path), Body: signingBody(req.Body, expire), Headers: req.Headers,
		Timestamp: timestamp, Nonce: parts[4],
	}
	if err := requestauth.Verify(cfg.SecretKey, material, parts[5]); err != nil {
		return Claims{}, fmt.Errorf("invalid signature: %w", err)
	}
	return Claims{
		AccessKey: parts[1], Timestamp: timestamp, Nonce: parts[4],
		TTL: time.Duration(expire+skew) * time.Second,
	}, nil
}

func normalizedPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func signingBody(body []byte, expire int64) []byte {
	result := make([]byte, 0, len(body)+40)
	result = append(result, body...)
	result = append(result, "\nmoox-service-expire:"...)
	return strconv.AppendInt(result, expire, 10)
}

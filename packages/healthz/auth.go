package healthz

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
)

const healthAuthHeader = "X-Moox-Health-Auth"

type AuthConfig struct {
	Version   string
	AccessKey string
	SecretKey string
	// ResetAccessKey 与 ResetSecretKey 是可选的第二对密钥：用它签名的请求带有 reset 权限（见 requestauth.PermissionReset），
	// 可以触发破坏性的重置；运行时的 AccessKey 没有这个权限。权限由服务端的密钥映射决定。两者都为空表示不开放 reset 权限。
	ResetAccessKey string
	ResetSecretKey string
	ClockSkew      time.Duration
	NonceTTL       time.Duration
	MaxNonces      int
}

type Authenticator struct {
	cfg    AuthConfig
	now    func() time.Time
	mu     sync.Mutex
	nonces map[string]time.Time
}

func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	if cfg.Version == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("health authentication version, access key and secret key are required")
	}
	if (cfg.ResetAccessKey == "") != (cfg.ResetSecretKey == "") {
		return nil, errors.New("health reset access key and secret key must be set together")
	}
	if cfg.ResetAccessKey != "" && cfg.ResetAccessKey == cfg.AccessKey {
		return nil, errors.New("health reset access key must differ from the runtime access key")
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = time.Minute
	}
	if cfg.NonceTTL <= 0 {
		cfg.NonceTTL = 2 * time.Minute
	}
	if cfg.MaxNonces <= 0 {
		cfg.MaxNonces = 10_000
	}
	return &Authenticator{cfg: cfg, now: time.Now, nonces: make(map[string]time.Time)}, nil
}

func AuthConfigFromEnv() (AuthConfig, error) {
	cfg := AuthConfig{
		Version:   envDefault("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1"),
		AccessKey: strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY")),
		SecretKey: strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY")),
		// 重置专用密钥：只配置在允许运维触发重置的模块进程上，不配置则该模块不接受任何重置请求。
		ResetAccessKey: strings.TrimSpace(os.Getenv("MOOX_HEALTH_RESET_ACCESS_KEY")),
		ResetSecretKey: strings.TrimSpace(os.Getenv("MOOX_HEALTH_RESET_SECRET_KEY")),
		ClockSkew:      time.Minute,
		NonceTTL:       2 * time.Minute,
		MaxNonces:      10_000,
	}
	if raw := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_CLOCK_SKEW")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			return AuthConfig{}, err
		}
		cfg.ClockSkew = value
	}
	_, err := NewAuthenticator(cfg)
	return cfg, err
}

func WrapFromEnv(next http.Handler) (http.Handler, error) {
	cfg, err := AuthConfigFromEnv()
	if err != nil {
		return nil, err
	}
	authenticator, err := NewAuthenticator(cfg)
	if err != nil {
		return nil, err
	}
	return authenticator.Wrap(next), nil
}

func (a *Authenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || next == nil {
			http.Error(w, "health authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		timestamp, nonce, signature, secret, reset, ok := a.parseHeader(r.Header.Get(healthAuthHeader))
		if !ok || absDuration(a.now().Sub(time.Unix(timestamp, 0))) > a.cfg.ClockSkew {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		material := requestauth.Material{Method: r.Method, Path: r.URL.EscapedPath(), Timestamp: timestamp, Nonce: nonce}
		if err := requestauth.Verify(secret, material, signature); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if status := a.consumeNonce(nonce); status != http.StatusOK {
			http.Error(w, http.StatusText(status), status)
			return
		}
		if reset {
			r = r.WithContext(requestauth.WithPermissions(r.Context(), requestauth.PermissionReset))
		}
		next.ServeHTTP(w, r)
	})
}

// parseHeader 解析鉴权头并按访问密钥选出验签用的密钥与权限：运行时密钥没有额外权限，重置专用密钥带 reset 权限。
func (a *Authenticator) parseHeader(raw string) (timestamp int64, nonce, signature, secret string, reset, ok bool) {
	parts := strings.Split(raw, "/")
	if len(parts) != 5 || parts[0] != a.cfg.Version {
		return 0, "", "", "", false, false
	}
	switch {
	case parts[1] == a.cfg.AccessKey:
		secret = a.cfg.SecretKey
	case a.cfg.ResetAccessKey != "" && parts[1] == a.cfg.ResetAccessKey:
		secret, reset = a.cfg.ResetSecretKey, true
	default:
		return 0, "", "", "", false, false
	}
	timestamp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || timestamp <= 0 {
		return 0, "", "", "", false, false
	}
	return timestamp, parts[3], parts[4], secret, reset, true
}

func (a *Authenticator) consumeNonce(nonce string) int {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for value, expiresAt := range a.nonces {
		if !expiresAt.After(now) {
			delete(a.nonces, value)
		}
	}
	if _, exists := a.nonces[nonce]; exists {
		return http.StatusUnauthorized
	}
	if len(a.nonces) >= a.cfg.MaxNonces {
		return http.StatusServiceUnavailable
	}
	a.nonces[nonce] = now.Add(a.cfg.NonceTTL)
	return http.StatusOK
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

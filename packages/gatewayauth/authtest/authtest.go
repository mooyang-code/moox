// Package authtest 给各模块的测试提供确定的签名身份与单身份校验，只用于测试。
package authtest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// Key 由标签派生一把固定的私钥。
func Key(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("moox-test-only/" + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

// Component 是部署在 host 上的组件的签名身份，用这台主机的测试密钥。
func Component(caller, host string) gatewayauth.Credentials {
	return gatewayauth.Credentials{Caller: caller, Host: host, Key: Key("host/" + host)}
}

// Identity 是不属于主机的身份（moox-cli 与外部身份）。
func Identity(name string) gatewayauth.Credentials {
	return gatewayauth.Credentials{Caller: name, Key: Key("identity/" + name)}
}

// Keys 返回只认这几个签名身份的校验表。
func Keys(credentials ...gatewayauth.Credentials) *gatewayauth.Keys {
	hosts, identities := map[string]gatewayauth.HostKey{}, map[string]ed25519.PublicKey{}
	for _, credential := range credentials {
		if credential.Host == "" {
			identities[credential.Caller] = credential.PublicKey()
			continue
		}
		host := hosts[credential.Host]
		host.PublicKey, host.Callers = credential.PublicKey(), append(host.Callers, credential.Caller)
		hosts[credential.Host] = host
	}
	keys, err := gatewayauth.NewKeys(hosts, identities)
	if err != nil {
		panic(err)
	}
	return keys
}

// Verify 用只认 credentials 的校验表校验一次调用。
func Verify(credentials gatewayauth.Credentials, request gatewayauth.Request, header http.Header, now time.Time) (gatewayauth.Claims, error) {
	return Keys(credentials).Verify(request, header, now)
}

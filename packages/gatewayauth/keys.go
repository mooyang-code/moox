package gatewayauth

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// HostKey 是一台主机的签名公钥，连同部署在这台主机上、可以用它签名的调用方（组件 ID；部署了 admin 的主机另有 console）。
type HostKey struct {
	PublicKey ed25519.PublicKey
	Callers   []string
}

// Keys 是校验表：主机公钥加不属于主机的身份的公钥。表里没有秘密。
type Keys struct {
	hosts      map[string]HostKey
	identities map[string]ed25519.PublicKey
}

// NewKeys 复制并校验输入。同一个名字不能既是某台主机上的调用方、又是不属于主机的身份，
// 否则拿到身份私钥就能冒充组件。
func NewKeys(hosts map[string]HostKey, identities map[string]ed25519.PublicKey) (*Keys, error) {
	keys := &Keys{hosts: make(map[string]HostKey, len(hosts)), identities: make(map[string]ed25519.PublicKey, len(identities))}
	for id, key := range identities {
		if !validIdentifier(id) || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("身份 %q 的公钥不合法", id)
		}
		keys.identities[id] = slices.Clone(key)
	}
	for id, host := range hosts {
		if !validIdentifier(id) || len(host.PublicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("主机 %q 的公钥不合法", id)
		}
		for _, caller := range host.Callers {
			if _, clash := keys.identities[caller]; clash || !validIdentifier(caller) {
				return nil, fmt.Errorf("主机 %q 上的调用方 %q 不合法或与非主机身份重名", id, caller)
			}
		}
		keys.hosts[id] = HostKey{PublicKey: slices.Clone(host.PublicKey), Callers: slices.Clone(host.Callers)}
	}
	return keys, nil
}

// Verify 校验一次调用的签名与时间窗：带签名主机的取该主机的公钥，并要求调用方确实部署在这台主机上；
// 不带的取非主机身份的公钥。防重放与“不早于进程启动”由 Replay 另行检查。
func (k *Keys) Verify(req Request, header http.Header, now time.Time) (Claims, error) {
	if k == nil {
		return Claims{}, errors.New("没有校验表")
	}
	caller, err := singleHeader(header, headerCaller)
	if err != nil {
		return Claims{}, err
	}
	host, err := optionalHeader(header, headerHost)
	if err != nil {
		return Claims{}, err
	}
	if !validIdentifier(caller) {
		return Claims{}, errors.New("调用方不合法")
	}
	var public ed25519.PublicKey
	if host == "" {
		key, ok := k.identities[caller]
		if !ok {
			return Claims{}, errors.New("没有登记这个身份的公钥")
		}
		public = key
	} else {
		key, ok := k.hosts[host]
		if !ok {
			return Claims{}, errors.New("没有登记这台主机的公钥")
		}
		if !slices.Contains(key.Callers, caller) {
			return Claims{}, errors.New("调用方不在签名主机上")
		}
		public = key.PublicKey
	}
	return verify(public, caller, host, req, header, now)
}

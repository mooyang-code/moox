package gatewayauth

import (
	"fmt"
	"net/http"
	"time"
)

type CredentialRegistry struct{ entries map[string]Credentials }

func NewCredentialRegistry(credentials []Credentials) (*CredentialRegistry, error) {
	// 同一调用方可以有多把密钥：轮换期间新旧 KeyID 同时有效。
	entries := make(map[string]Credentials, len(credentials))
	for _, credential := range credentials {
		if _, _, err := validateCredentials(credential); err != nil {
			return nil, err
		}
		if !validIdentifier(credential.Caller) {
			return nil, fmt.Errorf("credential caller %q is invalid", credential.Caller)
		}
		if _, exists := entries[credential.KeyID]; exists {
			return nil, fmt.Errorf("duplicate gateway key_id %q", credential.KeyID)
		}
		entries[credential.KeyID] = credential
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("gateway credential registry is empty")
	}
	return &CredentialRegistry{entries: entries}, nil
}

func (registry *CredentialRegistry) Verify(req Request, header http.Header, now time.Time) (Claims, error) {
	keyID, err := singleHeader(header, headerKeyID)
	if err != nil {
		return Claims{}, err
	}
	credential, ok := registry.entries[keyID]
	if !ok {
		return Claims{}, fmt.Errorf("unknown gateway key ID %q", keyID)
	}
	return Verify(credential, req, header, now)
}

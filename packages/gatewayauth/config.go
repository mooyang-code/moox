package gatewayauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CredentialRegistry struct{ entries map[string]Credentials }

type credentialRegistryFile struct {
	Version     int `json:"version"`
	Credentials []struct {
		KeyID      string `json:"key_id"`
		Caller     string `json:"caller"`
		SecretFile string `json:"secret_file"`
	} `json:"credentials"`
}

func NewCredentialRegistry(credentials []Credentials) (*CredentialRegistry, error) {
	entries := make(map[string]Credentials, len(credentials))
	callers := make(map[string]string, len(credentials))
	for _, credential := range credentials {
		if _, _, err := validateCredentials(credential); err != nil {
			return nil, err
		}
		if !validIdentifier(credential.Caller) {
			return nil, fmt.Errorf("credential caller %q is invalid", credential.Caller)
		}
		if previous, exists := callers[credential.Caller]; exists {
			return nil, fmt.Errorf("duplicate gateway caller %q for key IDs %q and %q", credential.Caller, previous, credential.KeyID)
		}
		if _, exists := entries[credential.KeyID]; exists {
			return nil, fmt.Errorf("duplicate gateway key_id %q", credential.KeyID)
		}
		entries[credential.KeyID] = credential
		callers[credential.Caller] = credential.KeyID
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("gateway credential registry is empty")
	}
	return &CredentialRegistry{entries: entries}, nil
}

func LoadCredentialRegistry(path string) (*CredentialRegistry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat gateway credential registry: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("gateway credential registry %s must be a regular 0600 file", path)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gateway credential registry: %w", err)
	}
	var file credentialRegistryFile
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode gateway credential registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode gateway credential registry: multiple JSON values are not allowed")
		}
		return nil, fmt.Errorf("decode gateway credential registry trailing content: %w", err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("gateway credential registry version must be 1")
	}
	credentials := make([]Credentials, 0, len(file.Credentials))
	for _, item := range file.Credentials {
		secretPath := item.SecretFile
		if !filepath.IsAbs(secretPath) {
			secretPath = filepath.Join(filepath.Dir(path), secretPath)
		}
		credential, err := CredentialsFromKeyFile(item.KeyID, secretPath)
		if err != nil {
			return nil, err
		}
		credential.Caller = strings.TrimSpace(item.Caller)
		credentials = append(credentials, credential)
	}
	return NewCredentialRegistry(credentials)
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

// CredentialsFromKeyFile loads a service secret without accepting symlinks or
// group/world-readable files. The gateway and every native tRPC client use the
// same fail-closed file contract.
func CredentialsFromKeyFile(keyID, path string) (Credentials, error) {
	keyID = strings.TrimSpace(keyID)
	path = strings.TrimSpace(path)
	if keyID == "" || path == "" {
		return Credentials{}, fmt.Errorf("gateway key_id and hmac_key_file are required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("stat gateway hmac key file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return Credentials{}, fmt.Errorf("gateway hmac key file %s must be a regular 0600 file", path)
	}
	secret, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("read gateway hmac key file: %w", err)
	}
	secretText := strings.TrimSpace(string(secret))
	if secretText == "" {
		return Credentials{}, fmt.Errorf("gateway hmac key file %s is empty", path)
	}
	return Credentials{KeyID: keyID, Secret: secretText}, nil
}

// ResolveCredentials prefers an explicitly configured key file and otherwise
// uses the process-injected credential pair for local and SCF runtimes.
func ResolveCredentials(keyID, keyFile string) (Credentials, error) {
	if strings.TrimSpace(keyFile) != "" {
		return CredentialsFromKeyFile(keyID, keyFile)
	}
	credentials := CredentialsFromEnv()
	if strings.TrimSpace(credentials.KeyID) == "" && strings.TrimSpace(keyID) != "" {
		credentials.KeyID = strings.TrimSpace(keyID)
	}
	if _, _, err := validateCredentials(credentials); err != nil {
		return Credentials{}, fmt.Errorf("gateway credentials are required: %w", err)
	}
	return credentials, nil
}

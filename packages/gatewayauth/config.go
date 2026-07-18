package gatewayauth

import (
	"fmt"
	"os"
	"strings"
)

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
	if strings.TrimSpace(keyID) != "" {
		credentials.KeyID = strings.TrimSpace(keyID)
	}
	return credentials, nil
}

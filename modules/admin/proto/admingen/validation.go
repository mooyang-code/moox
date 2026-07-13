package adminpb

import (
	"fmt"
	"strings"
)

func (r *CreateSecretReq) Validate() error {
	if r == nil || r.Secret == nil {
		return fmt.Errorf("secret is required")
	}
	if strings.TrimSpace(r.Secret.Name) == "" {
		return fmt.Errorf("secret.name is required")
	}
	if strings.TrimSpace(r.Secret.SecretValue) == "" {
		return fmt.Errorf("secret.secret_value is required")
	}
	return nil
}

func (r *LoginReq) Validate() error {
	if r == nil || strings.TrimSpace(r.Username) == "" {
		return fmt.Errorf("username is required")
	}
	if strings.TrimSpace(r.PasswordHash) == "" || strings.TrimSpace(r.Salt) == "" {
		return fmt.Errorf("password_hash and salt are required")
	}
	return nil
}

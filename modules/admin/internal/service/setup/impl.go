package setup

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	adminspace "github.com/mooyang-code/moox/modules/admin/internal/service/space"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gorm.io/gorm"
)

func (s *Service) Apply(ctx context.Context, manifest Manifest) (Result, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	result := expectedResult(manifest)
	if err := validateManifest(manifest, s.db, s.encryptionKey); err != nil {
		return Result{}, err
	}
	spaces, err := normalizeSpaces(manifest.Spaces)
	if err != nil {
		return Result{}, err
	}
	created := 0
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		n, err := s.ensureAdmin(ctx, tx, manifest.Admin)
		if err != nil {
			return err
		}
		created += n
		n, err = s.ensureTencentSecret(ctx, tx, manifest.TencentCloud)
		if err != nil {
			return err
		}
		created += n
		for _, space := range spaces {
			n, err = s.ensureSpace(ctx, tx, space)
			if err != nil {
				return err
			}
			created += n
			result.SpacesCreated += n
		}
		return nil
	})
	if err != nil {
		normalized := normalizeError(err)
		if errors.Is(normalized, ErrStorage) {
			status, inspectErr := s.Inspect(ctx, manifest)
			if inspectErr == nil && status.State == "completed" {
				result.Action = "unchanged"
				return result, nil
			}
			if inspectErr == nil && status.State == "conflict" {
				return Result{}, ErrConflict
			}
		}
		return Result{}, normalized
	}
	result.Action = "unchanged"
	if created > 0 {
		result.Action = "created"
	}
	result.SpacesUnchanged = result.Spaces - result.SpacesCreated
	return result, nil
}

func (s *Service) Inspect(ctx context.Context, manifest Manifest) (Status, error) {
	if err := validateManifest(manifest, s.db, s.encryptionKey); err != nil {
		return Status{}, err
	}
	spaces, err := normalizeSpaces(manifest.Spaces)
	if err != nil {
		return Status{}, err
	}
	status := Status{State: "completed", Users: 1, Secrets: 1, Spaces: len(spaces)}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := s.inspectAdmin(ctx, tx, manifest.Admin)
		if err != nil {
			return err
		}
		status.add(state)
		state, err = s.inspectTencentSecret(ctx, tx, manifest.TencentCloud)
		if err != nil {
			return err
		}
		status.add(state)
		for _, space := range spaces {
			state, err = s.inspectSpace(ctx, tx, space)
			if err != nil {
				return err
			}
			status.add(state)
		}
		return nil
	})
	if err != nil {
		return Status{}, normalizeError(err)
	}
	if status.Conflicts > 0 {
		status.State = "conflict"
	} else if status.Missing > 0 {
		status.State = "incomplete"
	}
	return status, nil
}

func expectedResult(manifest Manifest) Result {
	return Result{
		Users: 1, Secrets: 1,
		Spaces: len(manifest.Spaces), SpacesUnchanged: len(manifest.Spaces),
	}
}

func validateManifest(manifest Manifest, db *gorm.DB, encryptionKey string) error {
	if db == nil || encryptionKey == "" || strings.TrimSpace(manifest.Admin.Username) == "" || manifest.Admin.Password == "" ||
		strings.TrimSpace(manifest.TencentCloud.SecretID) == "" || manifest.TencentCloud.SecretKey == "" {
		return ErrInvalid
	}
	if _, err := normalizeSpaces(manifest.Spaces); err != nil {
		return err
	}
	return nil
}

type recordState uint8

const (
	recordMatch recordState = iota
	recordMissing
	recordConflict
)

func (s *Status) add(state recordState) {
	switch state {
	case recordMissing:
		s.Missing++
	case recordConflict:
		s.Conflicts++
	}
}

func (s *Service) ensureAdmin(ctx context.Context, tx *gorm.DB, input Admin) (int, error) {
	state, err := s.inspectAdmin(ctx, tx, input)
	if err != nil || state == recordMatch {
		return 0, err
	}
	if state == recordConflict {
		return 0, ErrConflict
	}
	hash, err := mooxsecurity.HashPassword(input.Password)
	if err != nil {
		return 0, ErrInvalid
	}
	now := time.Now()
	user := &authmodel.User{
		UserID: uuid.NewString(), Username: strings.TrimSpace(input.Username), PasswordHash: hash,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(user).Error; err != nil {
		return 0, ErrStorage
	}
	if err := s.afterWrite("admin"); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Service) inspectAdmin(ctx context.Context, tx *gorm.DB, input Admin) (recordState, error) {
	var user authmodel.User
	err := tx.WithContext(ctx).Where("c_username = ?", strings.TrimSpace(input.Username)).First(&user).Error
	if err == nil {
		if mooxsecurity.VerifyPassword(input.Password, user.PasswordHash) {
			return recordMatch, nil
		}
		return recordConflict, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return recordConflict, ErrStorage
	}
	// 系统只有一个超级用户：库里已有别的用户就是冲突。
	var otherUsers int64
	if err := tx.WithContext(ctx).Model(&authmodel.User{}).Count(&otherUsers).Error; err != nil {
		return recordConflict, ErrStorage
	}
	if otherUsers > 0 {
		return recordConflict, nil
	}
	return recordMissing, nil
}

func (s *Service) ensureTencentSecret(ctx context.Context, tx *gorm.DB, input TencentCloud) (int, error) {
	state, err := s.inspectTencentSecret(ctx, tx, input)
	if err != nil || state == recordMatch {
		return 0, err
	}
	if state == recordConflict {
		return 0, ErrConflict
	}
	encrypted, err := mooxsecurity.Encrypt(input.SecretKey, s.encryptionKey)
	if err != nil {
		return 0, ErrStorage
	}
	now := time.Now()
	secret := &secretmodel.Secret{
		SecretID: tencentSecretID, Name: "Tencent Cloud Default", Category: "cloud", Provider: "tencent",
		SecretType: "api_key", KeyID: strings.TrimSpace(input.SecretID), SecretValue: encrypted,
		ExtraConfig: "{}", Status: "active", Creator: "setup", CreateTime: now, ModifyTime: now,
	}
	if err := tx.WithContext(ctx).Create(secret).Error; err != nil {
		return 0, ErrStorage
	}
	if err := s.afterWrite("secret"); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Service) inspectTencentSecret(ctx context.Context, tx *gorm.DB, input TencentCloud) (recordState, error) {
	var secret secretmodel.Secret
	err := tx.WithContext(ctx).Where("c_secret_id = ? AND c_is_deleted = 0", tencentSecretID).First(&secret).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return recordMissing, nil
	}
	if err != nil {
		return recordConflict, ErrStorage
	}
	plain, err := mooxsecurity.Decrypt(secret.SecretValue, s.encryptionKey)
	if err != nil {
		return recordConflict, ErrStorage
	}
	metadataMatches := secret.Name == "Tencent Cloud Default" && secret.Category == "cloud" && secret.Provider == "tencent" &&
		secret.SecretType == "api_key" && secret.KeyID == strings.TrimSpace(input.SecretID) && secret.Status == "active"
	if metadataMatches && secretEqual(plain, input.SecretKey) {
		return recordMatch, nil
	}
	return recordConflict, nil
}

func (s *Service) ensureSpace(ctx context.Context, tx *gorm.DB, input Space) (int, error) {
	state, err := s.inspectSpace(ctx, tx, input)
	if err != nil || state == recordMatch {
		return 0, err
	}
	if state == recordConflict {
		return 0, ErrConflict
	}
	now := time.Now()
	item := &adminspace.Space{
		SpaceID: input.SpaceID, Name: input.Name, Description: input.Description,
		Owner: input.Owner, Market: input.Market, Timezone: input.Timezone,
		Status: input.Status, Attributes: input.AttributesJSON,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(item).Error; err != nil {
		return 0, ErrStorage
	}
	if err := s.afterWrite("space:" + input.SpaceID); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Service) inspectSpace(ctx context.Context, tx *gorm.DB, input Space) (recordState, error) {
	var stored adminspace.Space
	err := tx.WithContext(ctx).
		Where("c_space_id = ? AND c_is_deleted = 0", input.SpaceID).
		First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return recordMissing, nil
	}
	if err != nil {
		return recordConflict, ErrStorage
	}
	storedAttributes, err := canonicalJSONObject(stored.Attributes)
	if err != nil {
		return recordConflict, nil
	}
	if stored.SpaceID == input.SpaceID &&
		stored.Name == input.Name &&
		stored.Description == input.Description &&
		stored.Owner == input.Owner &&
		stored.Market == input.Market &&
		stored.Timezone == input.Timezone &&
		stored.Status == input.Status &&
		storedAttributes == input.AttributesJSON {
		return recordMatch, nil
	}
	return recordConflict, nil
}

func secretEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func normalizeSpaces(inputs []Space) ([]Space, error) {
	spaces := make([]Space, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		input.SpaceID = strings.TrimSpace(input.SpaceID)
		input.Name = strings.TrimSpace(input.Name)
		input.Description = strings.TrimSpace(input.Description)
		input.Owner = strings.TrimSpace(input.Owner)
		input.Market = strings.TrimSpace(input.Market)
		input.Timezone = strings.TrimSpace(input.Timezone)
		input.Status = strings.TrimSpace(input.Status)
		if input.Status == "" {
			input.Status = "active"
		}
		attributes, err := canonicalJSONObject(input.AttributesJSON)
		if err != nil || input.SpaceID == "" || input.Name == "" || input.Market == "" || input.Timezone == "" {
			return nil, ErrInvalid
		}
		if _, ok := seen[input.SpaceID]; ok {
			return nil, ErrInvalid
		}
		seen[input.SpaceID] = struct{}{}
		input.AttributesJSON = attributes
		spaces = append(spaces, input)
	}
	return spaces, nil
}

func canonicalJSONObject(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "{}", nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return "", ErrInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", ErrInvalid
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalid
	}
	return string(encoded), nil
}

func (s *Service) afterWrite(stage string) error {
	if s.writeHook == nil {
		return nil
	}
	if err := s.writeHook(stage); err != nil {
		return ErrStorage
	}
	return nil
}

func normalizeError(err error) error {
	switch {
	case errors.Is(err, ErrConflict):
		return ErrConflict
	case errors.Is(err, ErrInvalid):
		return ErrInvalid
	default:
		return ErrStorage
	}
}

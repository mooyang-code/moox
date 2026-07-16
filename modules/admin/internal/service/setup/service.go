package setup

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	sshmodel "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	mooxcrypto "github.com/mooyang-code/moox/packages/crypto"
	"gorm.io/gorm"
)

const tencentSecretID = "tencent-default"

var (
	ErrConflict = errors.New("setup_conflict")
	ErrStorage  = errors.New("setup_storage_failed")
	ErrInvalid  = errors.New("setup_invalid")
)

type Admin struct {
	Username string
	Password string
}

type TencentCloud struct {
	SecretID  string
	SecretKey string
}

type Host struct {
	Name     string
	Address  string
	Port     int
	Username string
	Password string
}

type Manifest struct {
	Admin        Admin
	TencentCloud TencentCloud
	ControlHost  Host
	OtherHosts   []Host
}

func (m Manifest) Hosts() []Host {
	hosts := make([]Host, 0, 1+len(m.OtherHosts))
	hosts = append(hosts, m.ControlHost)
	hosts = append(hosts, m.OtherHosts...)
	return hosts
}

type Result struct {
	Action  string
	Users   int
	Secrets int
	Hosts   int
}

type Status struct {
	State     string
	Users     int
	Secrets   int
	Hosts     int
	Missing   int
	Conflicts int
}

type Service struct {
	db            *gorm.DB
	encryptionKey string
	writeHook     func(string) error
}

func NewService(db *gorm.DB, encryptionKey string) *Service {
	return &Service{db: db, encryptionKey: encryptionKey}
}

func (s *Service) Apply(ctx context.Context, manifest Manifest) (Result, error) {
	result := expectedResult(manifest)
	if err := validateManifest(manifest, s.db, s.encryptionKey); err != nil {
		return Result{}, err
	}
	created := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		for _, host := range manifest.Hosts() {
			n, err = s.ensureHost(ctx, tx, host)
			if err != nil {
				return err
			}
			created += n
		}
		return nil
	})
	if err != nil {
		return Result{}, normalizeError(err)
	}
	result.Action = "unchanged"
	if created > 0 {
		result.Action = "created"
	}
	return result, nil
}

func (s *Service) Inspect(ctx context.Context, manifest Manifest) (Status, error) {
	if err := validateManifest(manifest, s.db, s.encryptionKey); err != nil {
		return Status{}, err
	}
	status := Status{State: "completed", Users: 1, Secrets: 1, Hosts: len(manifest.Hosts())}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		for _, host := range manifest.Hosts() {
			state, err = s.inspectHost(ctx, tx, host)
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
	return Result{Users: 1, Secrets: 1, Hosts: len(manifest.Hosts())}
}

func validateManifest(manifest Manifest, db *gorm.DB, encryptionKey string) error {
	if db == nil || encryptionKey == "" || strings.TrimSpace(manifest.Admin.Username) == "" || manifest.Admin.Password == "" ||
		strings.TrimSpace(manifest.TencentCloud.SecretID) == "" || manifest.TencentCloud.SecretKey == "" || len(manifest.Hosts()) == 0 {
		return ErrInvalid
	}
	for _, host := range manifest.Hosts() {
		if strings.TrimSpace(host.Name) == "" || strings.TrimSpace(host.Address) == "" || host.Port < 1 || host.Port > 65535 ||
			strings.TrimSpace(host.Username) == "" || host.Password == "" {
			return ErrInvalid
		}
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
	hash, err := mooxcrypto.HashPassword(input.Password)
	if err != nil {
		return 0, ErrInvalid
	}
	now := time.Now()
	user := &authmodel.User{
		UserID: uuid.NewString(), Username: strings.TrimSpace(input.Username), PasswordHash: hash,
		Role: 3, Status: 1, LastPasswordChange: now, CreatedAt: now, UpdatedAt: now,
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
	err := tx.WithContext(ctx).Where("c_username = ? AND c_is_deleted = 0", strings.TrimSpace(input.Username)).First(&user).Error
	if err == nil {
		if user.Role == 3 && user.Status == 1 && mooxcrypto.VerifyPassword(input.Password, user.PasswordHash) {
			return recordMatch, nil
		}
		return recordConflict, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return recordConflict, ErrStorage
	}
	var otherAdmins int64
	if err := tx.WithContext(ctx).Model(&authmodel.User{}).
		Where("c_role = 3 AND c_status = 1 AND c_is_deleted = 0").Count(&otherAdmins).Error; err != nil {
		return recordConflict, ErrStorage
	}
	if otherAdmins > 0 {
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
	encrypted, err := mooxcrypto.Encrypt(input.SecretKey, s.encryptionKey)
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
	plain, err := mooxcrypto.Decrypt(secret.SecretValue, s.encryptionKey)
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

func (s *Service) ensureHost(ctx context.Context, tx *gorm.DB, input Host) (int, error) {
	state, err := s.inspectHost(ctx, tx, input)
	if err != nil || state == recordMatch {
		return 0, err
	}
	if state == recordConflict {
		return 0, ErrConflict
	}
	encrypted, err := mooxcrypto.Encrypt(input.Password, s.encryptionKey)
	if err != nil {
		return 0, ErrStorage
	}
	now := time.Now()
	host := &sshmodel.SSHHost{
		Name: strings.TrimSpace(input.Name), Address: strings.TrimSpace(input.Address), Port: input.Port,
		User: strings.TrimSpace(input.Username), Password: encrypted, AuthType: "pwd", NetType: "tcp4",
		FontSize: 14, Background: "#000000", Foreground: "#FFFFFF", CursorColor: "#FFFFFF",
		FontFamily: "Courier New", CursorStyle: "block", Shell: "bash", PtyType: "xterm-256color",
		Creator: "setup", CreateTime: now, ModifyTime: now,
	}
	if err := tx.WithContext(ctx).Create(host).Error; err != nil {
		return 0, ErrStorage
	}
	if err := s.afterWrite("host:" + host.Name); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Service) inspectHost(ctx context.Context, tx *gorm.DB, input Host) (recordState, error) {
	var host sshmodel.SSHHost
	err := tx.WithContext(ctx).
		Where("c_address = ? OR c_name = ?", strings.TrimSpace(input.Address), strings.TrimSpace(input.Name)).
		First(&host).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return recordMissing, nil
	}
	if err != nil {
		return recordConflict, ErrStorage
	}
	plain, err := mooxcrypto.Decrypt(host.Password, s.encryptionKey)
	if err != nil {
		return recordConflict, ErrStorage
	}
	metadataMatches := host.Name == strings.TrimSpace(input.Name) && host.Address == strings.TrimSpace(input.Address) &&
		host.Port == input.Port && host.User == strings.TrimSpace(input.Username) && host.AuthType == "pwd"
	if metadataMatches && secretEqual(plain, input.Password) {
		return recordMatch, nil
	}
	return recordConflict, nil
}

func secretEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
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

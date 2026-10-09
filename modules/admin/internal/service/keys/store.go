// Package keys owns the encrypted master copies of gateway signing keys.
// Deployments reuse Current; Rotate starts an overlap, and Retire ends it only
// after the operator has confirmed the replacement is in use.
package keys

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

var (
	ErrInvalidCaller   = errors.New("unregistered gateway caller")
	ErrKeyNotFound     = errors.New("gateway key is not provisioned")
	ErrInvalidKeyring  = errors.New("invalid encrypted gateway keyring")
	ErrRotationPending = errors.New("previous gateway key must be retired before another rotation")
	ErrActiveKey       = errors.New("cannot retire the current signing key")
)

const maxRingBytes = 16 << 10

// SigningKey deliberately omits the secret from formatting and JSON. Exporters
// must explicitly request Credentials and write its Secret to a private file.
type SigningKey struct {
	Caller string `json:"caller"`
	KeyID  string `json:"key_id"`
	secret string
}

func (k SigningKey) Credentials() gatewayauth.Credentials {
	return gatewayauth.Credentials{Caller: k.Caller, KeyID: k.KeyID, Secret: k.secret}
}

func (k SigningKey) String() string {
	return fmt.Sprintf("SigningKey{caller:%s key_id:%s}", k.Caller, k.KeyID)
}
func (k SigningKey) GoString() string { return k.String() }

type KeyInfo struct {
	KeyID     string `json:"key_id"`
	Current   bool   `json:"current"`
	CreatedAt int64  `json:"created_at_unix"`
}

type Info struct {
	Caller string    `json:"caller"`
	Keys   []KeyInfo `json:"keys"`
}

// VerificationKey carries material only to snapshot/Access exporters. Both
// current and retiring keys remain valid until explicit retirement.
type VerificationKey struct {
	Caller string `json:"caller"`
	KeyID  string `json:"key_id"`
	Secret []byte `json:"-"`
}

func (k VerificationKey) String() string {
	return fmt.Sprintf("VerificationKey{caller:%s key_id:%s}", k.Caller, k.KeyID)
}
func (k VerificationKey) GoString() string { return k.String() }

type entry struct {
	KeyID     string `json:"key_id"`
	Secret    string `json:"secret"`
	CreatedAt int64  `json:"created_at_unix"`
}

type keyring struct {
	Version int     `json:"version"`
	Caller  string  `json:"caller"`
	Current string  `json:"current_key_id"`
	Entries []entry `json:"keys"`
}

type Store struct {
	db      *gorm.DB
	master  string
	catalog servicecatalog.Catalog
}

func (*Store) String() string     { return "GatewayKeyStore{encrypted master copies}" }
func (s *Store) GoString() string { return s.String() }

// NewStore never creates schema, generates a master key, or seeds callers.
func NewStore(db *gorm.DB, encryptionKey string) (*Store, error) {
	if db == nil || strings.TrimSpace(encryptionKey) == "" || encryptionKey != strings.TrimSpace(encryptionKey) {
		return nil, errors.New("gateway keys require database and encryption key")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	return &Store{db: db, master: encryptionKey, catalog: catalog}, nil
}

func (s *Store) validateCaller(tx *gorm.DB, caller string) error {
	if strings.HasPrefix(caller, "host-gateway@") {
		host := strings.TrimPrefix(caller, "host-gateway@")
		if !servicecatalog.ValidHostID(host) {
			return ErrInvalidCaller
		}
		var count int64
		if err := tx.Table("t_hosts").Where("c_host_id = ?", host).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrInvalidCaller
		}
		return nil
	}
	if caller == "console" || caller == "moox-cli" {
		return nil
	}
	// Gateways have a per-host identity, never a fleet-wide signing key.
	if _, ok := s.catalog.Component(caller); ok && caller != "host-gateway" {
		return nil
	}
	if s.isExternal(caller) {
		return nil
	}
	return ErrInvalidCaller
}

func (s *Store) isExternal(caller string) bool {
	return slices.ContainsFunc(s.catalog.Principals, func(p servicecatalog.Principal) bool { return p.ID == caller })
}

func (s *Store) mutate(ctx context.Context, fn func(*gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Reserve SQLite's writer before read/modify/write, including an empty
		// table, so independent live/offline stores cannot lose a rotation.
		if err := tx.Exec("UPDATE t_secrets SET c_id = c_id WHERE 0").Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func (s *Store) read(tx *gorm.DB, caller string) (keyring, error) {
	var rows []model.Secret
	if err := tx.Where("c_secret_id = ?", model.GatewayKeyringIDPrefix+caller).Find(&rows).Error; err != nil {
		return keyring{}, err
	}
	if len(rows) == 0 {
		return keyring{}, ErrKeyNotFound
	}
	if len(rows) != 1 {
		return keyring{}, ErrInvalidKeyring
	}
	row := rows[0]
	if row.IsDeleted || row.SecretType != model.GatewayKeyringType || row.Category != "gateway" || row.Provider != "moox" || row.Status != "active" || len(row.SecretValue) > 2*maxRingBytes {
		return keyring{}, ErrInvalidKeyring
	}
	plain, err := security.Decrypt(row.SecretValue, s.master)
	if err != nil || len(plain) > maxRingBytes {
		return keyring{}, ErrInvalidKeyring
	}
	var ring keyring
	decoder := json.NewDecoder(strings.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ring); err != nil {
		return keyring{}, ErrInvalidKeyring
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return keyring{}, ErrInvalidKeyring
	}
	if !validRing(ring, caller) || row.KeyID != ring.Current {
		return keyring{}, ErrInvalidKeyring
	}
	return ring, nil
}

func validRing(ring keyring, caller string) bool {
	if ring.Version != 1 || ring.Caller != caller || len(ring.Entries) < 1 || len(ring.Entries) > 2 {
		return false
	}
	seen := map[string]bool{}
	current := false
	for _, item := range ring.Entries {
		id, err := hex.DecodeString(item.KeyID)
		if err != nil || len(id) != 16 || item.KeyID != strings.ToLower(item.KeyID) || seen[item.KeyID] || item.CreatedAt <= 0 {
			return false
		}
		secret, err := base64.RawURLEncoding.DecodeString(item.Secret)
		if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != item.Secret {
			return false
		}
		seen[item.KeyID] = true
		current = current || item.KeyID == ring.Current
	}
	return current
}

func newEntry() (entry, error) {
	id := make([]byte, 16)
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, id); err != nil {
		return entry{}, errors.New("generate gateway key ID")
	}
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return entry{}, errors.New("generate gateway signing key")
	}
	return entry{KeyID: hex.EncodeToString(id), Secret: base64.RawURLEncoding.EncodeToString(secret), CreatedAt: time.Now().Unix()}, nil
}

func (s *Store) write(tx *gorm.DB, ring keyring, create bool) error {
	raw, err := json.Marshal(ring)
	if err != nil {
		return err
	}
	ciphertext, err := security.Encrypt(string(raw), s.master)
	if err != nil {
		return errors.New("encrypt gateway keyring")
	}
	id := model.GatewayKeyringIDPrefix + ring.Caller
	if create {
		return tx.Create(&model.Secret{
			SpaceID: "mooxsys", SecretID: id, Name: "Gateway caller " + ring.Caller,
			Category: "gateway", Provider: "moox", SecretType: model.GatewayKeyringType,
			KeyID: ring.Current, SecretValue: ciphertext, Status: "active", ExtraConfig: "{}",
			Creator: "gateway-keys", CreateTime: time.Now().UTC(), ModifyTime: time.Now().UTC(),
		}).Error
	}
	result := tx.Model(&model.Secret{}).Where("c_secret_id = ? AND c_is_deleted = 0", id).
		Updates(map[string]any{"c_key_id": ring.Current, "c_secret_value": ciphertext, "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvalidKeyring
	}
	return nil
}

func currentKey(ring keyring) SigningKey {
	for _, item := range ring.Entries {
		if item.KeyID == ring.Current {
			return SigningKey{Caller: ring.Caller, KeyID: item.KeyID, secret: item.Secret}
		}
	}
	return SigningKey{} // read always validates the current entry.
}

func (s *Store) ensure(tx *gorm.DB, caller string) (SigningKey, error) {
	ring, err := s.read(tx, caller)
	if err == nil {
		return currentKey(ring), nil
	}
	if !errors.Is(err, ErrKeyNotFound) {
		return SigningKey{}, err
	}
	item, err := newEntry()
	if err != nil {
		return SigningKey{}, err
	}
	ring = keyring{Version: 1, Caller: caller, Current: item.KeyID, Entries: []entry{item}}
	if err := s.write(tx, ring, true); err != nil {
		return SigningKey{}, err
	}
	return currentKey(ring), nil
}

func (s *Store) Ensure(ctx context.Context, caller string) (SigningKey, error) {
	var key SigningKey
	err := s.mutate(ctx, func(tx *gorm.DB) error {
		if err := s.validateCaller(tx, caller); err != nil {
			return err
		}
		var err error
		key, err = s.ensure(tx, caller)
		return err
	})
	if err != nil {
		return SigningKey{}, err
	}
	return key, nil
}

// EnsureAll provisions the catalog's callers and all registered host gateway
// identities in one transaction. Existing material is never regenerated.
func (s *Store) EnsureAll(ctx context.Context) ([]SigningKey, error) {
	var keys []SigningKey
	err := s.mutate(ctx, func(tx *gorm.DB) error {
		callers := []string{"console", "moox-cli"}
		for _, component := range s.catalog.Components {
			if component.ID != "host-gateway" {
				callers = append(callers, component.ID)
			}
		}
		for _, principal := range s.catalog.Principals {
			callers = append(callers, principal.ID)
		}
		var hosts []string
		if err := tx.Table("t_hosts").Order("c_host_id").Pluck("c_host_id", &hosts).Error; err != nil {
			return err
		}
		if len(hosts) > 1024 {
			return ErrInvalidCaller
		}
		for _, host := range hosts {
			if !servicecatalog.ValidHostID(host) {
				return ErrInvalidCaller
			}
			callers = append(callers, "host-gateway@"+host)
		}
		sort.Strings(callers)
		for _, caller := range callers {
			key, err := s.ensure(tx, caller)
			if err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func (s *Store) Current(ctx context.Context, caller string) (SigningKey, error) {
	var key SigningKey
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.validateCaller(tx, caller); err != nil {
			return err
		}
		ring, err := s.read(tx, caller)
		if err == nil {
			key = currentKey(ring)
		}
		return err
	})
	if err != nil {
		return SigningKey{}, err
	}
	return key, nil
}

func (s *Store) Rotate(ctx context.Context, caller string) (SigningKey, error) {
	var key SigningKey
	err := s.mutate(ctx, func(tx *gorm.DB) error {
		if err := s.validateCaller(tx, caller); err != nil {
			return err
		}
		ring, err := s.read(tx, caller)
		if err != nil {
			return err
		}
		if len(ring.Entries) == 2 {
			return ErrRotationPending
		}
		item, err := newEntry()
		if err != nil {
			return err
		}
		ring.Current = item.KeyID
		ring.Entries = append(ring.Entries, item)
		if err := s.write(tx, ring, false); err != nil {
			return err
		}
		key = currentKey(ring)
		return nil
	})
	if err != nil {
		return SigningKey{}, err
	}
	return key, nil
}

func (s *Store) Retire(ctx context.Context, caller, keyID string) error {
	return s.mutate(ctx, func(tx *gorm.DB) error {
		if err := s.validateCaller(tx, caller); err != nil {
			return err
		}
		ring, err := s.read(tx, caller)
		if err != nil {
			return err
		}
		if ring.Current == keyID {
			return ErrActiveKey
		}
		index := slices.IndexFunc(ring.Entries, func(item entry) bool { return item.KeyID == keyID })
		if index < 0 {
			return ErrKeyNotFound
		}
		ring.Entries = slices.Delete(ring.Entries, index, index+1)
		return s.write(tx, ring, false)
	})
}

func (s *Store) Info(ctx context.Context, caller string) (Info, error) {
	var info Info
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.validateCaller(tx, caller); err != nil {
			return err
		}
		ring, err := s.read(tx, caller)
		if err != nil {
			return err
		}
		info.Caller = ring.Caller
		for _, item := range ring.Entries {
			info.Keys = append(info.Keys, KeyInfo{KeyID: item.KeyID, Current: item.KeyID == ring.Current, CreatedAt: item.CreatedAt})
		}
		slices.SortFunc(info.Keys, func(a, b KeyInfo) int { return strings.Compare(a.KeyID, b.KeyID) })
		return nil
	})
	return info, err
}

// InternalVerification never accepts external principals. The caller list must
// come from the target host's compiled ACL, not all callers in the fleet.
func (s *Store) InternalVerification(ctx context.Context, callers []string) ([]VerificationKey, error) {
	return s.verification(ctx, callers, false)
}

func (s *Store) ExternalVerification(ctx context.Context) ([]VerificationKey, error) {
	callers := make([]string, 0, len(s.catalog.Principals))
	for _, principal := range s.catalog.Principals {
		callers = append(callers, principal.ID)
	}
	return s.verification(ctx, callers, true)
}

func (s *Store) verification(ctx context.Context, callers []string, external bool) ([]VerificationKey, error) {
	if len(callers) > 1100 {
		return nil, ErrInvalidCaller
	}
	callers = slices.Clone(callers)
	sort.Strings(callers)
	callers = slices.Compact(callers)
	keys := []VerificationKey{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, caller := range callers {
			if s.isExternal(caller) != external {
				return ErrInvalidCaller
			}
			if err := s.validateCaller(tx, caller); err != nil {
				return err
			}
			ring, err := s.read(tx, caller)
			if err != nil {
				return err
			}
			for _, item := range ring.Entries {
				keys = append(keys, VerificationKey{Caller: caller, KeyID: item.KeyID, Secret: bytes.Clone([]byte(item.Secret))})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(keys, func(a, b VerificationKey) int {
		if a.Caller != b.Caller {
			return strings.Compare(a.Caller, b.Caller)
		}
		return strings.Compare(a.KeyID, b.KeyID)
	})
	return keys, nil
}

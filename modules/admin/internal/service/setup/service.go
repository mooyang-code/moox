package setup

import (
	"errors"
	"sync"

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

type Space struct {
	SpaceID        string
	Name           string
	Description    string
	Owner          string
	Market         string
	Timezone       string
	Status         string
	AttributesJSON string
}

type Manifest struct {
	Admin        Admin
	TencentCloud TencentCloud
	ControlHost  Host
	OtherHosts   []Host
	Spaces       []Space
}

func (m Manifest) Hosts() []Host {
	hosts := make([]Host, 0, 1+len(m.OtherHosts))
	hosts = append(hosts, m.ControlHost)
	hosts = append(hosts, m.OtherHosts...)
	return hosts
}

type Result struct {
	Action          string
	Users           int
	Secrets         int
	Hosts           int
	Spaces          int
	SpacesCreated   int
	SpacesUnchanged int
}

type Status struct {
	State     string
	Users     int
	Secrets   int
	Hosts     int
	Spaces    int
	Missing   int
	Conflicts int
}

// Service coordinates the idempotent initial data setup flow.
// Implementation details live in impl.go so this file remains the public contract.
type Service struct {
	db            *gorm.DB
	encryptionKey string
	writeHook     func(string) error
	applyMu       sync.Mutex
}

func NewService(db *gorm.DB, encryptionKey string) *Service {
	return &Service{db: db, encryptionKey: encryptionKey}
}

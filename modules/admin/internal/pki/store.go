// Package pki owns the persistent MooX private CA and deployment-time host
// certificate issuance. It never replaces an existing or partial CA.
package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

const caCommonName = "MooX Private CA"

var ErrInvalidCA = errors.New("MooX CA is missing, incomplete, invalid, or expired; restore the original CA rather than regenerating it")

type CertificateInfo struct {
	SHA256   string    `json:"sha256"`
	Serial   string    `json:"serial"`
	NotAfter time.Time `json:"not_after"`
}

type CAInfo struct {
	CertificateInfo
	Created bool `json:"created"`
}

type HostIdentity struct {
	HostID         string
	Address        string
	PrivateAddress string
}

type IssuedCertificate struct {
	CertificateInfo
	HostID        string `json:"host_id"`
	CAFingerprint string `json:"ca_sha256"`
	Certificate   string `json:"certificate_file"`
	Key           string `json:"key_file"`
	CA            string `json:"ca_file"`
}

type Store struct {
	root *os.Root
	now  func() time.Time
}

func Open(dir string) (*Store, error) {
	root, err := privatefiles.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Store{root: root, now: time.Now}, nil
}

func (s *Store) Close() error     { return s.root.Close() }
func (*Store) String() string     { return "MooXPKI{persistent private CA}" }
func (s *Store) GoString() string { return s.String() }

// lock coordinates distinct CLI processes as well as callers in one process.
// The OS releases it on process exit; the persistent file is not a stale lock.
func (s *Store) lock() (*os.File, error) {
	return privatefiles.Lock(s.root, ".pki.lock")
}

func certificateInfo(cert *x509.Certificate) CertificateInfo {
	digest := sha256.Sum256(cert.Raw)
	return CertificateInfo{SHA256: hex.EncodeToString(digest[:]), Serial: cert.SerialNumber.Text(16), NotAfter: cert.NotAfter.UTC()}
}

func serialNumber() (*big.Int, error) {
	// A positive random 128-bit serial avoids both zero and predictable values.
	number, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	if number.Sign() == 0 {
		return serialNumber()
	}
	return number, nil
}

func singlePEM(raw []byte, kind string) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	block, rest := pem.Decode(raw)
	if !bytes.HasPrefix(raw, []byte("-----BEGIN "+kind+"-----")) || block == nil || block.Type != kind || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalidCA
	}
	return block.Bytes, nil
}

func (s *Store) loadCA() (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certPEM, certErr := privatefiles.ReadAt(s.root, "ca.crt", 64<<10)
	keyPEM, keyErr := privatefiles.ReadAt(s.root, "ca.key", 64<<10)
	if certErr != nil || keyErr != nil {
		return nil, nil, nil, ErrInvalidCA
	}
	certDER, certErr := singlePEM(certPEM, "CERTIFICATE")
	keyDER, keyErr := singlePEM(keyPEM, "PRIVATE KEY")
	if certErr != nil || keyErr != nil {
		return nil, nil, nil, ErrInvalidCA
	}
	cert, certErr := x509.ParseCertificate(certDER)
	parsed, keyErr := x509.ParsePKCS8PrivateKey(keyDER)
	key, ok := parsed.(*ecdsa.PrivateKey)
	now := s.now()
	if certErr != nil || keyErr != nil || !ok || key.Curve != elliptic.P256() || !cert.IsCA || !cert.BasicConstraintsValid ||
		cert.KeyUsage&x509.KeyUsageCertSign == 0 || cert.Subject.CommonName != caCommonName ||
		now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) || !key.PublicKey.Equal(cert.PublicKey) || cert.CheckSignatureFrom(cert) != nil {
		return nil, nil, nil, ErrInvalidCA
	}
	if _, err := s.root.Lstat("ca.sha256"); err == nil {
		marker, err := privatefiles.ReadAt(s.root, "ca.sha256", 256)
		if err != nil || string(bytes.TrimSpace(marker)) != certificateInfo(cert).SHA256 {
			return nil, nil, nil, ErrInvalidCA
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, nil, ErrInvalidCA
	}
	return cert, key, certPEM, nil
}

// Info validates an existing CA without creating or repairing trust material.
// Normal host deployments must use this after the initial offline bootstrap.
func (s *Store) Info() (CAInfo, error) {
	lock, err := s.lock()
	if err != nil {
		return CAInfo{}, err
	}
	defer lock.Close()
	cert, _, _, err := s.loadCA()
	if err != nil {
		return CAInfo{}, err
	}
	return CAInfo{CertificateInfo: certificateInfo(cert)}, nil
}

func (s *Store) EnsureCA() (CAInfo, error) {
	lock, err := s.lock()
	if err != nil {
		return CAInfo{}, err
	}
	defer lock.Close()
	_, certErr := s.root.Lstat("ca.crt")
	_, keyErr := s.root.Lstat("ca.key")
	if !os.IsNotExist(certErr) || !os.IsNotExist(keyErr) {
		cert, _, _, err := s.loadCA()
		if err != nil {
			return CAInfo{}, err
		}
		if _, err := s.root.Lstat("ca.sha256"); os.IsNotExist(err) {
			if err := privatefiles.Write(s.root, "ca.sha256", []byte(certificateInfo(cert).SHA256+"\n")); err != nil {
				return CAInfo{}, err
			}
		}
		return CAInfo{CertificateInfo: certificateInfo(cert)}, nil
	}
	// Public inventory/continuity metadata prove this is not a fresh store even
	// if both root files were lost. Restore them instead of changing trust.
	for _, name := range []string{"ca.sha256", "hosts"} {
		if _, err := s.root.Lstat(name); !os.IsNotExist(err) {
			return CAInfo{}, ErrInvalidCA
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CAInfo{}, err
	}
	serial, err := serialNumber()
	if err != nil {
		return CAInfo{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: caCommonName},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return CAInfo{}, err
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return CAInfo{}, err
	}
	// If publication is interrupted, the remaining half causes future calls to
	// fail closed. Never destroy the surviving root or silently change trust.
	if err := privatefiles.Write(s.root, "ca.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})); err != nil {
		return CAInfo{}, err
	}
	if err := privatefiles.Write(s.root, "ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return CAInfo{}, err
	}
	cert, _, _, err := s.loadCA()
	if err != nil {
		return CAInfo{}, err
	}
	if err := privatefiles.Write(s.root, "ca.sha256", []byte(certificateInfo(cert).SHA256+"\n")); err != nil {
		return CAInfo{}, err
	}
	return CAInfo{CertificateInfo: certificateInfo(cert), Created: true}, nil
}

func validateHost(host HostIdentity) error {
	if !servicecatalog.ValidHostID(host.HostID) || !servicecatalog.ValidHostAddress(host.Address) ||
		host.PrivateAddress != "" && !servicecatalog.ValidHostAddress(host.PrivateAddress) {
		return errors.New("host certificate requires canonical host ID and bare public/private IP or DNS addresses")
	}
	return nil
}

// Issue always creates a new key and certificate. The entire output directory
// is published together and must be absent; deployment then installs the bundle.
func (s *Store) Issue(host HostIdentity, outputDir string) (IssuedCertificate, error) {
	if err := validateHost(host); err != nil {
		return IssuedCertificate{}, err
	}
	lock, err := s.lock()
	if err != nil {
		return IssuedCertificate{}, err
	}
	defer lock.Close()
	ca, caKey, caPEM, err := s.loadCA()
	if err != nil {
		return IssuedCertificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return IssuedCertificate{}, err
	}
	serial, err := serialNumber()
	if err != nil {
		return IssuedCertificate{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	notAfter := now.AddDate(5, 0, 0)
	if ca.NotAfter.Before(notAfter) {
		notAfter = ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: host.HostID},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: notAfter,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{host.HostID},
	}
	for _, address := range []string{host.Address, host.PrivateAddress} {
		if address == "" {
			continue
		}
		if ip := net.ParseIP(address); ip != nil {
			if !slices.ContainsFunc(template.IPAddresses, func(existing net.IP) bool { return existing.Equal(ip) }) {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
		} else if name := strings.ToLower(address); !slices.Contains(template.DNSNames, name) {
			template.DNSNames = append(template.DNSNames, name)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return IssuedCertificate{}, err
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return IssuedCertificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})
	if err := publishBundle(outputDir, serial.Text(16), map[string][]byte{"server.crt": certPEM, "server.key": keyPEM, "moox-ca.crt": caPEM}); err != nil {
		return IssuedCertificate{}, err
	}
	// Keep public copies for Admin's certificate watch. CA and leaf private keys
	// are never loaded by that reader or stored in the database.
	hosts, err := privatefiles.OpenSubRoot(s.root, "hosts")
	if err != nil {
		return IssuedCertificate{}, err
	}
	defer hosts.Close()
	if err := privatefiles.Write(hosts, host.HostID+".crt", certPEM); err != nil {
		return IssuedCertificate{}, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return IssuedCertificate{}, err
	}
	return IssuedCertificate{
		CertificateInfo: certificateInfo(cert), HostID: host.HostID, CAFingerprint: certificateInfo(ca).SHA256,
		Certificate: filepath.Join(outputDir, "server.crt"), Key: filepath.Join(outputDir, "server.key"), CA: filepath.Join(outputDir, "moox-ca.crt"),
	}, nil
}

func publishBundle(dir, id string, files map[string][]byte) error {
	dir = filepath.Clean(dir)
	name := filepath.Base(dir)
	if name == "." || name == string(filepath.Separator) {
		return errors.New("certificate output must be a new directory")
	}
	parent, err := privatefiles.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer parent.Close()
	if _, err := parent.Lstat(name); !os.IsNotExist(err) {
		return errors.New("certificate output directory must not already exist")
	}
	stageName := ".issue-" + id
	if err := parent.Mkdir(stageName, 0o700); err != nil {
		return err
	}
	defer parent.RemoveAll(stageName)
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return err
	}
	defer stage.Close()
	for name, data := range files {
		if err := privatefiles.Write(stage, name, data); err != nil {
			return err
		}
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := parent.Rename(stageName, name); err != nil {
		return err
	}
	return privatefiles.SyncDirectory(parent)
}

// ExportCA publishes only the public root, after validating the original pair.
func (s *Store) ExportCA(outputDir string) (CertificateInfo, error) {
	lock, err := s.lock()
	if err != nil {
		return CertificateInfo{}, err
	}
	defer lock.Close()
	ca, _, raw, err := s.loadCA()
	if err != nil {
		return CertificateInfo{}, err
	}
	output, err := privatefiles.OpenRoot(outputDir)
	if err != nil {
		return CertificateInfo{}, err
	}
	defer output.Close()
	if err := privatefiles.Write(output, "moox-ca.crt", raw); err != nil {
		return CertificateInfo{}, err
	}
	return certificateInfo(ca), nil
}

package engine

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
)

func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":") + "\n"
}

func checkFingerprint(path string, cert *x509.Certificate) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := strings.TrimSpace(string(raw))
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		if len(parts) != sha256.Size {
			return errors.New("invalid CA fingerprint length")
		}
		for _, part := range parts {
			if len(part) != 2 {
				return errors.New("invalid CA fingerprint encoding")
			}
		}
		s = strings.Join(parts, "")
	}
	got, err := hex.DecodeString(s)
	if err != nil || len(got) != sha256.Size {
		return errors.New("invalid CA fingerprint encoding")
	}
	want := sha256.Sum256(cert.Raw)
	if !bytes.Equal(got, want[:]) {
		return errors.New("CA fingerprint mismatch; explicit certificate rotation is required")
	}
	return nil
}

func readCA(dir, name string) (*x509.Certificate, []byte, error) {
	crt, err := os.ReadFile(filepath.Join(dir, name+".crt"))
	if err != nil {
		return nil, nil, err
	}
	block, rest := pem.Decode(crt)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("persisted CA requires exactly one PEM certificate")
	}
	keyPath := filepath.Join(dir, name+".key")
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, nil, fmt.Errorf("CA key %s must not be accessible to group or others", name)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	pair, err := tls.X509KeyPair(crt, key)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid CA certificate/key pair %s: %w", name, err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, nil, errors.New("persisted certificate is not a signing CA")
	}
	return cert, crt, nil
}

func caDir(c config.Config) string {
	return filepath.Join(c.TLS.StorageRoot, "pki", "authorities", "local")
}

// prepareCA is called before Caddy Load, so corrupt/missing existing state
// cannot be silently replaced by Caddy's automatic CA creation.
func prepareCA(c config.Config) (*x509.Certificate, error) {
	cert, err := verifyCA(c, false)
	if err == nil && cert != nil {
		err = checkFingerprint(c.TLS.CABaseline, cert)
	}
	return cert, err
}

// CheckState validates persisted keys, certificates and the existing trust
// baseline without creating files, issuing certificates or starting listeners.
func CheckState(c config.Config) (string, error) {
	cert, err := prepareCA(c)
	if err != nil {
		return "", err
	}
	if cert == nil {
		if c.TLS.Mode == "internal" {
			return "", errors.New("internal CA state is absent")
		}
		return "", nil
	}
	return strings.TrimSpace(fingerprint(cert)), nil
}

// CheckImportState requires the original published certificate and fingerprint
// even when a copied baseline exists. It never writes the closed source tree.
func CheckImportState(c config.Config) (string, error) {
	cert, err := verifyCA(c, false)
	if err != nil {
		return "", err
	}
	if cert == nil {
		return "", errors.New("trusted import requires existing CA state")
	}
	if err := checkPublishedCA(c, cert); err != nil {
		return "", err
	}
	return strings.TrimSpace(fingerprint(cert)), nil
}

func verifyCA(c config.Config, allowImport bool) (*x509.Certificate, error) {
	dir := caDir(c)
	_, err := os.Stat(filepath.Join(dir, "root.crt"))
	if errors.Is(err, os.ErrNotExist) {
		// Any remnants prove this is not an empty first install.
		for _, path := range []string{dir, c.TLS.CABaseline, filepath.Join(c.TLS.CAPublishDir, "root.crt"), filepath.Join(c.TLS.CAPublishDir, "root.sha256")} {
			if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) {
				return nil, errors.New("incomplete existing CA state; refusing automatic replacement")
			}
		}
		if c.TLS.Mode == "internal" {
			return nil, errors.New("CA is missing; initialize or import state offline before serving")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cert, _, err := readCA(dir, "root")
	if err != nil {
		return nil, err
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		return nil, fmt.Errorf("invalid root CA signature: %w", err)
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return nil, errors.New("persisted root CA is outside its validity period")
	}
	intermediate, _, err := readCA(dir, "intermediate")
	if err != nil {
		return nil, err
	}
	if err := intermediate.CheckSignatureFrom(cert); err != nil {
		return nil, fmt.Errorf("invalid intermediate CA signature: %w", err)
	}
	// Caddy may renew an expired intermediate using the validated root. The
	// root's identity, not the short-lived intermediate's expiry, is frozen.
	if err := checkFingerprint(c.TLS.CABaseline, cert); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// Initial legacy import requires the trusted published certificate AND
		// its existing OpenSSL fingerprint, not a newly computed self-baseline.
		if err := checkPublishedCA(c, cert); err != nil {
			return nil, fmt.Errorf("trusted legacy CA import: %w", err)
		}
		if allowImport {
			if err := atomicWrite(c.TLS.CABaseline, []byte(fingerprint(cert)), 0600); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range []string{"root.crt", "root.sha256"} {
		if _, err := os.Stat(filepath.Join(c.TLS.CAPublishDir, name)); err == nil {
			if err := checkPublishedCA(c, cert); err != nil {
				return nil, err
			}
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return cert, nil
}

func checkPublishedCA(c config.Config, cert *x509.Certificate) error {
	raw, err := os.ReadFile(filepath.Join(c.TLS.CAPublishDir, "root.crt"))
	if err != nil {
		return err
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 || !bytes.Equal(block.Bytes, cert.Raw) {
		return errors.New("published CA does not match persistent CA")
	}
	return checkFingerprint(filepath.Join(c.TLS.CAPublishDir, "root.sha256"), cert)
}

func publishCA(c config.Config, previous *x509.Certificate) (*x509.Certificate, error) {
	// Public-only installations do not provision PKI. Any partial historical
	// state was rejected before Load; an empty state stays empty.
	if c.TLS.Mode == "public" && previous == nil {
		return nil, nil
	}
	cert, crt, err := readCA(caDir(c), "root")
	if err != nil {
		return nil, err
	}
	if previous != nil && !bytes.Equal(previous.Raw, cert.Raw) {
		return nil, errors.New("CA changed during provisioning")
	}
	if err := atomicWrite(c.TLS.CABaseline, []byte(fingerprint(cert)), 0600); err != nil {
		return nil, err
	}
	if c.TLS.Mode == "internal" {
		if err := atomicWrite(filepath.Join(c.TLS.CAPublishDir, "root.crt"), crt, 0644); err != nil {
			return nil, err
		}
		if err := atomicWrite(filepath.Join(c.TLS.CAPublishDir, "root.sha256"), []byte(fingerprint(cert)), 0644); err != nil {
			return nil, err
		}
	}
	return cert, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".console-proxy-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

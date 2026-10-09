package bootstrap

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

func openPublicPKIRoot(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("MooX public certificate directory is unavailable or not a regular directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("MooX public certificate directory cannot be opened")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("MooX public certificate directory changed while opening")
	}
	return root, nil
}

func readPublicCertificateBundle(root *os.Root, name string) ([]*x509.Certificate, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("public certificate file is unavailable or not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, errors.New("public certificate file cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("public certificate file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return nil, errors.New("public certificate file is unreadable or exceeds 1 MiB")
	}
	var certificates []*x509.Certificate
	for rest := bytes.TrimSpace(raw); len(rest) > 0; {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("public certificate file contains non-certificate data")
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("public certificate PEM is invalid")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("public certificate PEM is invalid")
		}
		if len(certificates) >= 16 {
			return nil, errors.New("public certificate bundle exceeds 16 certificates")
		}
		certificates = append(certificates, certificate)
		rest = bytes.TrimSpace(remaining)
	}
	if len(certificates) == 0 {
		return nil, errors.New("public certificate file contains no certificate")
	}
	return certificates, nil
}

func validateCertificateTimes(certificates []*x509.Certificate, now time.Time) error {
	for _, certificate := range certificates {
		if now.Before(certificate.NotBefore) {
			return errors.New("public certificate is not yet valid")
		}
		if !now.Before(certificate.NotAfter) {
			return fmt.Errorf("public certificate expired at %s", certificate.NotAfter.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

package unitbundle

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type accessCredential struct {
	KeyID      string `json:"key_id"`
	Caller     string `json:"caller"`
	SecretFile string `json:"secret_file"`
}

type accessTable struct {
	Version     int                `json:"version"`
	Credentials []accessCredential `json:"credentials"`
}

func signingSecret(raw []byte) (string, error) {
	secret := strings.TrimSpace(string(raw))
	if len(raw) > 4096 || len(secret) < 32 || !utf8.ValidString(secret) || strings.ContainsFunc(secret, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("host material signing secret is invalid")
	}
	return secret, nil
}

func verifyAccess(files map[string][]byte, name string, allowed map[string]bool) ([]gatewayauth.Credentials, error) {
	var table accessTable
	if err := canonical(files[name], &table); err != nil {
		return nil, err
	}
	if table.Version != 1 || len(table.Credentials) == 0 || len(table.Credentials) > 128 {
		return nil, errors.New("host material Access verification table is invalid")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	principals := map[string]bool{}
	for _, principal := range catalog.Principals {
		principals[principal.ID] = false
	}
	allowed[name] = true
	var result []gatewayauth.Credentials
	previous := ""
	for _, entry := range table.Credentials {
		_, external := principals[entry.Caller]
		order := entry.Caller + "\x00" + entry.KeyID
		if !external || !validKeyID(entry.KeyID) || order <= previous || entry.SecretFile != "caller-"+entry.Caller+"-"+entry.KeyID+".key" || !privatePath(entry.SecretFile) || path.Base(entry.SecretFile) != entry.SecretFile {
			return nil, errors.New("host material Access table contains an unrelated or misplaced identity")
		}
		previous = order
		keyPath := path.Join(path.Dir(name), entry.SecretFile)
		if allowed[keyPath] {
			return nil, errors.New("host material Access table repeats a key file")
		}
		secret, err := signingSecret(files[keyPath])
		if err != nil {
			return nil, err
		}
		allowed[keyPath] = true
		principals[entry.Caller] = true
		result = append(result, gatewayauth.Credentials{Caller: entry.Caller, KeyID: entry.KeyID, Secret: secret})
	}
	for _, present := range principals {
		if !present {
			return nil, errors.New("host material Access table is missing an external principal")
		}
	}
	return result, nil
}

func onePEM(raw []byte, kind string) ([]byte, error) {
	// Reject leading/trailing material as well as extra PEM blocks. In
	// particular a CA payload cannot carry a private key after its certificate.
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != kind || len(block.Headers) != 0 || !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(pem.EncodeToMemory(block))) || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("host material requires a single PEM block of the expected type")
	}
	return block.Bytes, nil
}

func certificate(raw []byte, info hostbundle.CertificateInfo) (*x509.Certificate, error) {
	der, err := onePEM(raw, "CERTIFICATE")
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || digest(der) != info.SHA256 || cert.SerialNumber.Text(16) != info.Serial || !cert.NotAfter.Equal(info.NotAfter) {
		return nil, errors.New("host material certificate differs from its public metadata")
	}
	return cert, nil
}

func verifyCertificates(metadata hostbundle.Metadata, files map[string][]byte, options Options) error {
	ca, err := certificate(files["certs/moox-ca.crt"], metadata.CA.CertificateInfo)
	if err != nil {
		return err
	}
	now := time.Now()
	if !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) || ca.CheckSignatureFrom(ca) != nil {
		return errors.New("host material CA is not a valid current self-signed trust root")
	}
	leaf, err := certificate(files["certs/host-gateway/server.crt"], metadata.Certificate)
	if err != nil {
		return err
	}
	if leaf.IsCA || leaf.Subject.CommonName != options.HostID || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
		return errors.New("host material leaf certificate does not identify its target host")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: options.HostID, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errors.New("host material leaf certificate does not verify under the pinned CA")
	}
	names, ips := []string{options.HostID}, []string{}
	for _, address := range []string{options.Address, options.PrivateAddress} {
		if address == "" {
			continue
		}
		if ip := net.ParseIP(address); ip != nil {
			ips = append(ips, ip.String())
		} else {
			names = append(names, strings.ToLower(address))
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	slices.Sort(ips)
	ips = slices.Compact(ips)
	actualNames := slices.Clone(leaf.DNSNames)
	actualIPs := make([]string, 0, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		actualIPs = append(actualIPs, ip.String())
	}
	slices.Sort(actualNames)
	slices.Sort(actualIPs)
	if !slices.Equal(names, actualNames) || !slices.Equal(ips, actualIPs) {
		return errors.New("host material certificate SAN differs from target public/private addresses")
	}
	key, err := onePEM(files["certs/host-gateway/server.key"], "PRIVATE KEY")
	if err != nil {
		return err
	}
	if _, err := x509.ParsePKCS8PrivateKey(key); err != nil {
		return errors.New("host material leaf private key is invalid")
	}
	if _, err := tls.X509KeyPair(files["certs/host-gateway/server.crt"], files["certs/host-gateway/server.key"]); err != nil {
		return errors.New("host material leaf private key does not match its certificate")
	}
	return nil
}

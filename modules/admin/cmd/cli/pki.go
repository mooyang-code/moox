package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
)

// moox-admin-cli pki ensure-ca | issue | export-ca 只供部署脚本内部调用（设计文档 3.5）。

const defaultPKIDir = "./secrets/pki"

func isPKICommand(args []string) bool { return len(args) > 1 && args[1] == "pki" }

func runPKICommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return errors.New("缺少 pki 子命令：ensure-ca、issue、export-ca")
	}
	fs := flag.NewFlagSet("pki", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir, host, out, outDir := defaultPKIDir, "", "", ""
	var addresses stringList
	fs.StringVar(&dir, "pki-dir", dir, "MooX 私有 CA 所在目录（secrets/pki）")
	fs.StringVar(&host, "host", "", "主机 ID")
	fs.Var(&addresses, "address", "主机地址，可重复")
	fs.StringVar(&out, "out", "", "导出 CA 证书的路径")
	fs.StringVar(&outDir, "out-dir", "", "主机网关证书输出目录")
	sub := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	now := time.Now()
	switch sub {
	case "ensure-ca":
		created, ca, err := pki.EnsureCA(dir, now)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "created": created, "not_after": ca.NotAfter.UTC().Format(time.RFC3339)})
	case "issue":
		if host == "" || outDir == "" {
			return errors.New("issue 需要 --host 和 --out-dir")
		}
		issued, err := issueHostCertificate(dir, host, addresses, outDir, now)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "host": host, "not_after": issued.Cert.NotAfter.UTC().Format(time.RFC3339)})
	case "export-ca":
		if out == "" {
			return errors.New("export-ca 需要 --out")
		}
		if err := exportCA(dir, out); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "out": out})
	default:
		return fmt.Errorf("未知的 pki 子命令 %q", sub)
	}
}

// issueHostCertificate 签发证书并写到 out-dir/server.crt、server.key。
func issueHostCertificate(dir, host string, addresses []string, outDir string, now time.Time) (pki.HostCertificate, error) {
	issued, err := pki.IssueHostCertificate(dir, host, addresses, now)
	if err != nil {
		return pki.HostCertificate{}, err
	}
	if err := pki.WriteSecretFile(filepath.Join(outDir, "server.key"), issued.KeyPEM); err != nil {
		return pki.HostCertificate{}, err
	}
	if err := pki.WriteSecretFile(filepath.Join(outDir, "server.crt"), issued.CertPEM); err != nil {
		return pki.HostCertificate{}, err
	}
	return issued, nil
}

func exportCA(dir, out string) error {
	raw, err := os.ReadFile(filepath.Join(dir, pki.CACertFile))
	if err != nil {
		return fmt.Errorf("读取 CA 证书: %w", err)
	}
	if _, err := pki.ParseCertificatePEM(raw); err != nil {
		return err
	}
	return pki.WriteSecretFile(out, raw)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*s = append(*s, item)
		}
	}
	return nil
}

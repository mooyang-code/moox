package deploy

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 控制台代理使用的 Caddy 版本；校验和来自 scripts/deps/caddy-v<版本>-checksums.txt。
const (
	caddyVersion   = "2.11.4"
	caddyChecksums = "scripts/deps/caddy-v2.11.4-checksums.txt"
	caddyURL       = "https://github.com/caddyserver/caddy/releases/download/v%s/%s"
	// EnvCaddyArchive 指定本机已下载的 Caddy 发布包（离线或网络慢时使用），仍会校验。
	EnvCaddyArchive = "MOOX_CADDY_ARCHIVE_CACHE"
)

// FetchCaddy 返回目标架构的 caddy 可执行文件：先用缓存，没有时下载 GitHub 发布包并按校验和验证。
func FetchCaddy(ctx context.Context, repositoryRoot, goarch string) (string, error) {
	archiveName := fmt.Sprintf("caddy_%s_linux_%s.tar.gz", caddyVersion, goarch)
	want, err := caddyChecksum(filepath.Join(repositoryRoot, caddyChecksums), archiveName)
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("找不到缓存目录: %w", err)
	}
	dir := filepath.Join(cache, "moox", "caddy", caddyVersion, goarch)
	binary := filepath.Join(dir, "caddy")
	marker := binary + ".sha512"
	if raw, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(raw)) == want {
		if _, err := os.Stat(binary); err == nil {
			return binary, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	archive := filepath.Join(dir, archiveName)
	if source := strings.TrimSpace(os.Getenv(EnvCaddyArchive)); source != "" {
		if err := copyFile(source, archive); err != nil {
			return "", fmt.Errorf("读取 %s: %w", EnvCaddyArchive, err)
		}
	} else if err := download(ctx, fmt.Sprintf(caddyURL, caddyVersion, archiveName), archive); err != nil {
		return "", err
	}
	got, err := fileSHA512(archive)
	if err != nil {
		return "", err
	}
	if got != want {
		_ = os.Remove(archive)
		return "", fmt.Errorf("Caddy 发布包 %s 校验失败", archiveName)
	}
	if err := extractCaddy(archive, binary); err != nil {
		return "", err
	}
	if err := os.WriteFile(marker, []byte(want+"\n"), 0o644); err != nil {
		return "", err
	}
	return binary, nil
}

func caddyChecksum(path, archiveName string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取 Caddy 校验和: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == archiveName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("Caddy 校验和中没有 %s", archiveName)
}

func download(ctx context.Context, url, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("下载 %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s: HTTP %d", url, response.StatusCode)
	}
	temporary := target + ".download"
	file, err := os.Create(temporary)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, response.Body); err != nil {
		_ = file.Close()
		return fmt.Errorf("下载 %s: %w", url, err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}

func fileSHA512(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha512.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractCaddy(archive, target string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("解压 %s: %w", archive, err)
	}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return fmt.Errorf("%s 中没有 caddy", archive)
		}
		if err != nil {
			return fmt.Errorf("解压 %s: %w", archive, err)
		}
		if header.Typeflag != tar.TypeReg || header.Name != "caddy" {
			continue
		}
		temporary := target + ".next"
		out, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(reader, header.Size)); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(temporary, target)
	}
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

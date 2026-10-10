package unitpackage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

const (
	unitManifestName = "package-manifest.json"
	maxUnitFiles     = 4096
	maxUnitBytes     = int64(1 << 30)
	maxUnitFileBytes = int64(512 << 20)
)

// Options consumes prebuilt executables. Packaging never compiles or
// reads the operator's manifest, SSH credentials, certificates, or runtime data.
type Options struct {
	RepositoryRoot  string
	BinaryDirectory string
	Output          string
	Profile         string
	GOOS            string
	GOARCH          string
}

type Component struct {
	ID     string `json:"id"`
	Binary string `json:"binary"`
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

// Manifest describes immutable software and configuration templates.
// Issued host material and rendered configuration belong to deployment.
type Manifest struct {
	Version       int         `json:"version"`
	Profile       string      `json:"profile"`
	GOOS          string      `json:"goos"`
	GOARCH        string      `json:"goarch"`
	CatalogSHA256 string      `json:"catalog_sha256"`
	Components    []Component `json:"components"`
	Files         []File      `json:"files"`
}

type Result struct {
	Archive  string   `json:"archive"`
	SHA256   string   `json:"sha256"`
	Manifest Manifest `json:"manifest"`
}

type unitAsset struct {
	source, destination string
	tree                bool
}
type unitComponentSpec struct {
	helpers []string
	assets  []unitAsset
}

var unitProfiles = map[string][]string{
	"host":         {"host-gateway", "host-agent"},
	"control":      {"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"},
	"storage":      {"storage-primary", "storage-node", "storage-view"},
	"access":       {"access"},
	"egress-proxy": {"egress-proxy"},
	"trade":        {"trade"},
}

func configAsset(module, component string) unitAsset {
	return unitAsset{"modules/" + module + "/config", component + "/config", true}
}
func schemaAsset(module, component string) unitAsset {
	return unitAsset{"modules/" + module + "/schema", component + "/schema", true}
}

var unitSpecs = map[string]unitComponentSpec{
	"host-gateway": {[]string{"moox-host-gateway-cli", "moox-runtime"}, []unitAsset{configAsset("hostgateway", "host-gateway")}},
	"host-agent": {[]string{"moox-host-agent-cli"}, []unitAsset{
		{"modules/hostagent/config/app.yaml", "host-agent/config/app.yaml", false},
		{"modules/hostagent/config/trpc_go.yaml", "host-agent/config/trpc_go.yaml", false},
	}},
	"console-proxy": {assets: []unitAsset{configAsset("consoleproxy", "console-proxy")}},
	"web-host":      {}, // The frontend is embedded in the prebuilt web-host executable.
	"admin":         {[]string{"moox-admin-cli"}, []unitAsset{configAsset("admin", "admin"), schemaAsset("admin", "admin")}},
	"eventbus":      {assets: []unitAsset{configAsset("eventbus", "eventbus")}},
	"monitor":       {[]string{"moox-monitor-cli"}, []unitAsset{configAsset("monitor", "monitor"), schemaAsset("monitor", "monitor")}},
	"collector": {[]string{"moox-collector-cli"}, []unitAsset{
		configAsset("collector", "collector"), schemaAsset("collector", "collector"),
		{"modules/collector/configs/sources", "collector/configs/sources", true},
	}},
	"cloudnode": {[]string{"moox-cloudnode-cli"}, []unitAsset{configAsset("cloudnode", "cloudnode"), schemaAsset("cloudnode", "cloudnode")}},
	"factor-mgr": {[]string{"moox-factor-mgr-cli"}, []unitAsset{
		{"modules/factor/config/app.yaml", "factor-mgr/config/app.yaml", false},
		{"modules/factor/config/trpc_go.yaml", "factor-mgr/config/trpc_go.yaml", false},
		schemaAsset("factor", "factor-mgr"),
		{"modules/factor/factors", "factor-mgr/factors", true},
	}},
	"strategy": {[]string{"moox-strategy-cli"}, []unitAsset{configAsset("strategy", "strategy"), schemaAsset("strategy", "strategy")}},
	"storage-primary": {[]string{"moox-storage-cli"}, []unitAsset{
		{"modules/storage/config/trpc_go.primary.yaml", "storage-primary/config/trpc_go.yaml", false},
		{"modules/storage/config/storage.primary.yaml", "storage-primary/config/storage.yaml", false},
		schemaAsset("storage", "storage-primary"),
	}},
	"storage-node": {assets: []unitAsset{
		{"modules/storage/config/trpc_go.node.yaml", "storage-node/config/trpc_go.yaml", false},
		{"modules/storage/config/storage.node.yaml", "storage-node/config/storage.yaml", false},
	}},
	"storage-view": {assets: []unitAsset{{"modules/storage/config/storage_view/trpc_go.yaml", "storage-view/config/trpc_go.yaml", false}}},
	"access":       {assets: []unitAsset{configAsset("access", "access")}},
	"egress-proxy": {assets: []unitAsset{configAsset("egressproxy", "egress-proxy")}},
	"trade":        {[]string{"moox-trade-cli"}, []unitAsset{configAsset("trade", "trade"), schemaAsset("trade", "trade")}},
}

// Components returns the exact process boundary of a deployment unit.
func Components(profile string) ([]Component, error) {
	ids, ok := unitProfiles[profile]
	if !ok {
		return nil, fmt.Errorf("unsupported deployment profile %q; use host, control, storage, access, egress-proxy, or trade", profile)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	components := make([]Component, 0, len(ids))
	for _, id := range ids {
		component, exists := catalog.Component(id)
		if !exists || component.Binary == "" {
			return nil, fmt.Errorf("deployment component %q is missing from the catalog", id)
		}
		if _, exists := unitSpecs[id]; !exists {
			return nil, fmt.Errorf("deployment component %q has no assets", id)
		}
		if (profile == "host") != (component.Scope == servicecatalog.ScopeHost) {
			return nil, fmt.Errorf("invalid host component boundary for %q", id)
		}
		components = append(components, Component{ID: id, Binary: "bin/" + component.Binary})
	}
	return components, nil
}

func Binaries(profile string) ([]string, error) {
	components, err := Components(profile)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, component := range components {
		names = append(names, path.Base(component.Binary))
		names = append(names, unitSpecs[component.ID].helpers...)
	}
	sort.Strings(names)
	return slices.Compact(names), nil
}

type unitSource struct {
	root   string
	arch   string
	source string
	data   []byte
	mode   fs.FileMode
}

func Package(ctx context.Context, opts Options) (result Result, returnErr error) {
	components, err := Components(opts.Profile)
	if err != nil {
		return result, err
	}
	if err := unitPlatform(opts.GOOS, opts.GOARCH); err != nil {
		return result, err
	}
	root, err := filepath.EvalSymlinks(opts.RepositoryRoot)
	if err != nil {
		return result, fmt.Errorf("deployment source root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return result, err
	}
	binaryRoot, err := filepath.EvalSymlinks(opts.BinaryDirectory)
	if err != nil {
		return result, fmt.Errorf("deployment binary directory: %w", err)
	}
	binaryRoot, err = filepath.Abs(binaryRoot)
	if err != nil {
		return result, err
	}
	tracked, err := unitTrackedFiles(ctx, root)
	if err != nil {
		return result, err
	}
	catalog := servicecatalog.EmbeddedYAML()
	catalogPath := "packages/servicecatalog/catalog.yaml"
	if !tracked[catalogPath] {
		return result, fmt.Errorf("deployment source catalog is not tracked")
	}
	sourceCatalog, err := unitReadSmall(root, catalogPath)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(catalog, sourceCatalog) {
		return result, fmt.Errorf("deployment source catalog differs from this CLI; rebuild moox-cli")
	}
	sources := map[string]unitSource{"config/servicecatalog/catalog.yaml": {data: catalog, mode: 0o644}}
	names, err := Binaries(opts.Profile)
	if err != nil {
		return result, err
	}
	for _, name := range names {
		file, err := unitOpenRegular(binaryRoot, name)
		if err != nil {
			return result, fmt.Errorf("deployment binary %s: %w", name, err)
		}
		var header [64]byte
		_, readErr := io.ReadFull(file, header[:])
		info, statErr := file.Stat()
		_ = file.Close()
		if readErr != nil || statErr != nil || info.Mode().Perm()&0o111 == 0 || !validUnitELF(header[:], opts.GOARCH) {
			return result, fmt.Errorf("deployment binary %s must be an executable %s/%s ELF file", name, opts.GOOS, opts.GOARCH)
		}
		sources["bin/"+name] = unitSource{root: binaryRoot, arch: opts.GOARCH, source: filepath.Join(binaryRoot, name), mode: 0o755}
	}
	for _, component := range components {
		for _, asset := range unitSpecs[component.ID].assets {
			found := 0
			for source := range tracked {
				if source != asset.source && (!asset.tree || !strings.HasPrefix(source, asset.source+"/")) {
					continue
				}
				if asset.tree && !unitAssetExtension(source) {
					continue
				}
				rel := strings.TrimPrefix(source, asset.source)
				destination := asset.destination + rel
				if _, duplicate := sources[destination]; duplicate {
					return result, fmt.Errorf("duplicate deployment asset %s", destination)
				}
				file, err := unitOpenRegular(root, source)
				if err != nil {
					return result, fmt.Errorf("deployment asset %s: %w", source, err)
				}
				_ = file.Close()
				sources[destination] = unitSource{root: root, source: filepath.Join(root, filepath.FromSlash(source)), mode: 0o644}
				found++
			}
			if found == 0 {
				return result, fmt.Errorf("missing tracked deployment assets: %s", asset.source)
			}
		}
	}
	if len(sources) > maxUnitFiles {
		return result, fmt.Errorf("deployment package exceeds file limit")
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil || !strings.HasSuffix(opts.Output, ".tar.gz") {
		return result, fmt.Errorf("deployment package output must end in .tar.gz")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return result, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return result, err
	}
	output = filepath.Join(parent, filepath.Base(output))
	if info, err := os.Lstat(output); err == nil && !info.Mode().IsRegular() {
		return result, fmt.Errorf("deployment package output must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, source := range sources {
		if source.source == output {
			return result, fmt.Errorf("deployment package output cannot replace a source file")
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return result, err
	}
	file, err := os.CreateTemp(filepath.Dir(output), ".moox-unit-*.tar.gz")
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	digest := sha256.New()
	compressed := gzip.NewWriter(io.MultiWriter(file, digest))
	writer := tar.NewWriter(compressed)
	manifest := Manifest{Version: 1, Profile: opts.Profile, GOOS: opts.GOOS, GOARCH: opts.GOARCH, CatalogSHA256: unitDigest(catalog), Components: components, Files: make([]File, 0, len(sources))}
	paths := make([]string, 0, len(sources))
	for name := range sources {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	var total int64
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entry, err := writeUnitSource(ctx, writer, name, sources[name])
		if err != nil {
			return result, err
		}
		total += entry.Size
		if total > maxUnitBytes {
			return result, fmt.Errorf("deployment package exceeds size limit")
		}
		manifest.Files = append(manifest.Files, entry)
	}
	entries := make(map[string]File, len(manifest.Files))
	for _, entry := range manifest.Files {
		entries[entry.Path] = entry
	}
	if err := validateUnitManifest(manifest, entries); err != nil {
		return result, err
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, err
	}
	payload = append(payload, '\n')
	if len(payload) > 4<<20 || total+int64(len(payload)) > maxUnitBytes {
		return result, fmt.Errorf("deployment package including metadata exceeds size limit")
	}
	if _, err = writeUnitSource(ctx, writer, unitManifestName, unitSource{data: payload, mode: 0o644}); err != nil {
		return result, err
	}
	if err = writer.Close(); err != nil {
		return result, err
	}
	if err = compressed.Close(); err != nil {
		return result, err
	}
	if err = file.Sync(); err != nil {
		return result, err
	}
	if err = file.Close(); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = os.Rename(file.Name(), output); err != nil {
		return result, err
	}
	return Result{Archive: output, SHA256: "sha256:" + hex.EncodeToString(digest.Sum(nil)), Manifest: manifest}, nil
}

func unitPlatform(goos, goarch string) error {
	if goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		return fmt.Errorf("deployment packages support linux/amd64 and linux/arm64")
	}
	return nil
}
func unitDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}
func unitAssetExtension(name string) bool {
	base := path.Base(name)
	if strings.HasPrefix(base, "test_") || base == "conftest.py" || strings.HasSuffix(base, "_test.py") {
		return false
	}
	return slices.Contains([]string{".yaml", ".yml", ".json", ".sql", ".py"}, path.Ext(name))
}
func validUnitPath(name string) bool {
	return name != "" && name != "." && path.Clean(name) == name && !path.IsAbs(name) && name != ".." && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n")
}
func unitTrackedFiles(ctx context.Context, root string) (map[string]bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z")
	raw, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("deployment source requires a Git checkout: %w", err)
	}
	files := make(map[string]bool)
	for _, name := range strings.Split(string(raw), "\x00") {
		if name == "" {
			continue
		}
		if !validUnitPath(name) {
			return nil, fmt.Errorf("invalid tracked deployment source path")
		}
		files[name] = true
	}
	return files, nil
}
func unitOpenRegular(root, name string) (*os.File, error) {
	if !validUnitPath(filepath.ToSlash(name)) {
		return nil, fmt.Errorf("invalid deployment source path")
	}
	current := root
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("deployment source cannot contain symbolic links")
		}
	}
	before, err := os.Lstat(current)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxUnitFileBytes {
		return nil, fmt.Errorf("deployment source must be a bounded regular file")
	}
	file, err := os.Open(current)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, fmt.Errorf("deployment source changed while opening")
	}
	return file, nil
}
func unitReadSmall(root, name string) ([]byte, error) {
	file, err := unitOpenRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, fmt.Errorf("deployment metadata is too large or unreadable")
	}
	return raw, nil
}
func validUnitELF(header []byte, goarch string) bool {
	if len(header) < 64 || !bytes.Equal(header[:4], []byte{0x7f, 'E', 'L', 'F'}) || header[4] != 2 || header[5] != 1 || header[6] != 1 {
		return false
	}
	kind := binary.LittleEndian.Uint16(header[16:18])
	machine := binary.LittleEndian.Uint16(header[18:20])
	return (kind == 2 || kind == 3) && ((goarch == "amd64" && machine == 62) || (goarch == "arm64" && machine == 183))
}
func writeUnitSource(ctx context.Context, writer *tar.Writer, name string, source unitSource) (File, error) {
	var reader io.Reader = bytes.NewReader(source.data)
	var file *os.File
	var before os.FileInfo
	size := int64(len(source.data))
	if source.source != "" {
		var err error
		relative, relErr := filepath.Rel(source.root, source.source)
		if relErr != nil {
			return File{}, relErr
		}
		file, err = unitOpenRegular(source.root, filepath.ToSlash(relative))
		if err != nil {
			return File{}, err
		}
		defer file.Close()
		before, err = file.Stat()
		if err != nil {
			return File{}, err
		}
		if source.arch != "" {
			var header [64]byte
			if _, err := io.ReadFull(file, header[:]); err != nil || before.Mode().Perm()&0o111 == 0 || !validUnitELF(header[:], source.arch) {
				return File{}, fmt.Errorf("deployment binary changed before packaging")
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return File{}, err
			}
		}
		size = before.Size()
		reader = file
	}
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: int64(source.mode.Perm()), Size: size, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
		return File{}, err
	}
	digest := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(&unitContextWriter{ctx: ctx, writer: writer}, digest), reader, size); err != nil {
		return File{}, err
	}
	if file != nil {
		after, err := file.Stat()
		if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return File{}, fmt.Errorf("deployment source changed while packaging")
		}
	}
	return File{Path: name, SHA256: "sha256:" + hex.EncodeToString(digest.Sum(nil)), Mode: uint32(source.mode.Perm()), Size: size}, nil
}

type unitContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w *unitContextWriter) Write(raw []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(raw)
}

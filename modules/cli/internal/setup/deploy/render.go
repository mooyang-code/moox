package deploy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"gopkg.in/yaml.v3"
)

// UnitTemplatePath 是统一的单元模板在仓库里的位置。
const UnitTemplatePath = "deploy/systemd/user/moox-component.service.tmpl"

// objectStoreEnvFile 是归档 worker 使用外部对象存储时在 secrets/ 下存放的凭据文件名。
const objectStoreEnvFile = "archive-object-store.env"

// defaultStreamSpace 是 collector-stream 实例服务的空间（同一空间只部署一个实例）。
const defaultStreamSpace = "crypto"

// localGatewayAddress 是本机主机网关给同机组件用的入口。
const localGatewayAddress = "127.0.0.1:11002"

// sqliteDatabaseFiles 是各 SQLite 业务组件在各自 data/<组件>/ 下的数据库文件名，配置键统一为 database.path。
var sqliteDatabaseFiles = map[string]string{
	"admin":       "admin.db",
	"monitor":     "monitor.db",
	"collector":   "moox_collector.db",
	"factor-mgr":  "factor.db",
	"archive-mgr": "archive.db",
	"strategy":    "strategy.sqlite",
	"trade":       "moox_trade.db",
}

// Identity 是从控制机取到并校验过的身份材料，按部署根布局摆放。
type Identity struct {
	// SignPublicKey 是这台主机登记的签名公钥；SCFCollectorPublicKey 只在部署了 collector 的主机上有。
	// 它们进组件的指纹：换了密钥，用它签名的组件就要重启；没换时私钥文件不在 Secrets 里，目标机上的原样沿用。
	SignPublicKey, SCFCollectorPublicKey string
	// Secrets 是 secrets/ 下这次要写的文件（相对路径 → 内容）：moox-ca.crt、host.crt、host.tls.key，新签发时的 host.key 与
	// scf-collector.key。
	Secrets map[string][]byte
	// EventBus 是导出的 EventBus 材料（文件名 → 内容）：<角色>.yaml，EventBus 所在主机另有 users.yaml。
	// 证书不在这里：EventBus 用 Secrets 里的主机证书，客户端用根证书校验。
	EventBus map[string][]byte
}

// Request 是一次渲染的输入。
type Request struct {
	Config  *topology.Config
	Catalog servicecatalog.Catalog
	Host    topology.Host
	// Components 是要渲染的组件；HostComponents 是这台主机上的全部组件（单元文件的 After= 只写同机的）。
	Components     []string
	HostComponents []string
	// ReadSource 读仓库（或产物包快照）里的一个文件，路径相对仓库根。
	ReadSource func(name string) ([]byte, error)
	Identity   Identity
	// CLS 是服务日志上报的写入密钥，只在 moox.toml 打开了 logging.cls 时使用。
	CLS topology.CLSWriter
	// ObjectStoreEnv 是归档 worker 使用外部对象存储时的凭据文件内容；未配 [archive_object_store] 时为空。
	ObjectStoreEnv []byte
}

// File 是要放到目标机上的一个文件。
type File struct {
	Data []byte
	Mode fs.FileMode
}

// Output 是渲染结果。Files 的键是相对部署根的路径（config/<组件>/…、secrets/…）。
type Output struct {
	Files map[string]File
	// Units 是单元文件名 → 内容。
	Units map[string][]byte
	// Fingerprints 是组件 → 这次渲染出的配置、环境文件、单元文件与它用到的秘密的摘要（不含二进制）。
	Fingerprints map[string]string
}

type renderer struct {
	Request
	layout Layout
	out    Output
	// used 记每个组件用到的文件（相对部署根），用来算指纹。
	used map[string][]string
}

// Render 渲染一组组件的配置、环境文件与单元文件，并在最后应用 [overrides]。
func Render(request Request) (Output, error) {
	if request.Config == nil || request.ReadSource == nil || request.Host.Name == "" {
		return Output{}, errors.New("渲染需要配置、主机与模板来源")
	}
	r := &renderer{Request: request, layout: Layout{Root: request.Config.Paths.DeployRoot}, used: map[string][]string{}}
	r.out = Output{Files: map[string]File{}, Units: map[string][]byte{}, Fingerprints: map[string]string{}}
	rawTemplate, err := request.ReadSource(UnitTemplatePath)
	if err != nil {
		return Output{}, fmt.Errorf("读单元模板 %s: %w", UnitTemplatePath, err)
	}
	unitTemplate, err := template.New("unit").Option("missingkey=error").Parse(string(rawTemplate))
	if err != nil {
		return Output{}, fmt.Errorf("单元模板 %s 无法解析: %w", UnitTemplatePath, err)
	}
	for name, raw := range request.Identity.Secrets {
		r.out.Files[path.Join("secrets", name)] = File{Data: raw, Mode: 0o600}
	}
	for _, id := range request.Components {
		component, ok := request.Catalog.Component(id)
		if !ok {
			return Output{}, fmt.Errorf("组件 %s 不在服务目录里", id)
		}
		if err := r.component(component, unitTemplate); err != nil {
			return Output{}, fmt.Errorf("渲染组件 %s: %w", id, err)
		}
	}
	if err := r.applyOverrides(); err != nil {
		return Output{}, err
	}
	for _, id := range request.Components {
		r.out.Fingerprints[id] = r.fingerprint(id)
	}
	return r.out, nil
}

func (r *renderer) put(component, name string, raw []byte, mode fs.FileMode) {
	r.out.Files[name] = File{Data: raw, Mode: mode}
	r.use(component, name)
}

func (r *renderer) use(component, name string) {
	if !slices.Contains(r.used[component], name) {
		r.used[component] = append(r.used[component], name)
	}
}

// fingerprint 对组件用到的文件（路径、权限、内容）、单元文件和它签名用的公钥算摘要。主机证书每次签发都不同，不算在内，
// 否则每次部署都会重启主机网关和 EventBus；证书本身照常覆盖，下次重启时生效。私钥文件只在新签发时才在包里，所以算公钥、不算私钥文件：
// 密钥没换时指纹不变，换了就变。
func (r *renderer) fingerprint(component string) string {
	names := slices.Clone(r.used[component])
	sort.Strings(names)
	hash := sha256.New()
	fmt.Fprintf(hash, "sign-public-key\x00%s\x00", r.Identity.SignPublicKey)
	if component == "collector" {
		fmt.Fprintf(hash, "scf-collector-public-key\x00%s\x00", r.Identity.SCFCollectorPublicKey)
	}
	for _, name := range names {
		if name == "secrets/host.crt" || name == "secrets/host.tls.key" || name == "secrets/host.key" || name == "secrets/scf-collector.key" {
			continue
		}
		file := r.out.Files[name]
		fmt.Fprintf(hash, "%s\x00%o\x00%d\x00", name, file.Mode, len(file.Data))
		hash.Write(file.Data)
	}
	unit := r.out.Units[UnitName(component)]
	fmt.Fprintf(hash, "unit\x00%d\x00", len(unit))
	hash.Write(unit)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func (r *renderer) component(component servicecatalog.Component, unitTemplate *template.Template) error {
	id := component.ID
	environment := map[string]string{
		"MOOX_NODE_ID":      r.Host.Name,
		"MOOX_SERVICE_NAME": id,
		"MOOX_INSTANCE_ID":  id + "@" + r.Host.Name,
	}
	if err := r.eventBus(component, environment); err != nil {
		return err
	}
	for _, file := range component.ConfigFiles {
		raw, err := r.ReadSource(file.From)
		if err != nil {
			return fmt.Errorf("读配置模板 %s: %w", file.From, err)
		}
		target := path.Join("config", id, file.Target())
		if path.Ext(file.Target()) != ".yaml" {
			r.put(id, target, raw, 0o600)
			continue
		}
		rendered, err := r.configFile(component, file.Target(), raw)
		if err != nil {
			return fmt.Errorf("%s: %w", file.Target(), err)
		}
		r.put(id, target, rendered, 0o600)
	}
	if err := r.generated(component, environment); err != nil {
		return err
	}
	r.environment(component, environment)
	envFile, err := encodeEnvironment(environment)
	if err != nil {
		return err
	}
	r.put(id, path.Join("secrets", "env", id+".env"), envFile, 0o600)
	unit, err := r.unit(component, unitTemplate)
	if err != nil {
		return err
	}
	r.out.Units[UnitName(id)] = unit
	for _, name := range r.secretsOf(component) {
		if _, ok := r.out.Files[name]; ok {
			r.use(id, name)
		}
	}
	return nil
}

// secretsOf 是组件用到的身份文件（相对部署根）。
func (r *renderer) secretsOf(component servicecatalog.Component) []string {
	names := []string{"secrets/moox-ca.crt"}
	// 主机证书是主机网关和 EventBus 的服务端证书。
	if component.ID == "host-gateway" || component.ID == "eventbus" {
		names = append(names, "secrets/host.crt", "secrets/host.tls.key")
	}
	return names
}

// eventBus 把组件要的 EventBus 角色文件写到 secrets/eventbus/，并在环境里写上连接地址与凭据位置。
func (r *renderer) eventBus(component servicecatalog.Component, environment map[string]string) error {
	roles := component.EventBusRoles()
	if len(roles) == 0 {
		return nil
	}
	url := r.Config.EventBusURLFor(r.Host.Name)
	directory := r.layout.Secret("eventbus")
	for _, role := range roles {
		raw, ok := r.Identity.EventBus[role+".yaml"]
		if !ok {
			return fmt.Errorf("身份材料里没有 EventBus 角色 %s", role)
		}
		document, err := parseYAML(raw)
		if err != nil {
			return fmt.Errorf("EventBus 角色 %s: %w", role, err)
		}
		if err := errors.Join(setPath(document, "urls", []string{url}, false), setPath(document, "ca_file", r.layout.Secret(hostbundle.CAFile), false)); err != nil {
			return fmt.Errorf("EventBus 角色 %s: %w", role, err)
		}
		rendered, err := encodeYAML(document)
		if err != nil {
			return err
		}
		r.put(component.ID, "secrets/eventbus/"+role+".yaml", rendered, 0o600)
	}
	environment["MOOX_METRICS_EVENTBUS_URL"] = url
	environment["MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE"] = path.Join(directory, "metrics-publisher.yaml")
	if role := primaryRole(component); role != "" {
		credential := path.Join(directory, role+".yaml")
		environment["MOOX_EVENTBUS_URL"], environment["MOOX_EVENTBUS_CREDENTIAL_FILE"] = url, credential
		switch component.ID {
		case "storage-primary", "storage-view":
			environment["MOOX_STORAGE_EVENTBUS_URL"], environment["MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE"] = url, credential
		case "monitor":
			environment["MOOX_OBSERVABILITY_EVENTBUS_URL"], environment["MOOX_OBSERVABILITY_CREDENTIAL_FILE"] = url, credential
		}
	}
	if component.ID == "eventbus" {
		raw, ok := r.Identity.EventBus[hostbundle.UsersFile]
		if !ok {
			return errors.New("身份材料里没有 EventBus 的用户表")
		}
		r.put(component.ID, "secrets/eventbus/"+hostbundle.UsersFile, raw, 0o600)
	}
	return nil
}

// primaryRole 是组件自己声明的 EventBus 角色（不含人人都有的 metrics-publisher）。
func primaryRole(component servicecatalog.Component) string {
	for _, credential := range component.Credentials {
		if role, ok := strings.CutPrefix(credential, "eventbus:"); ok {
			return role
		}
	}
	return ""
}

// environment 写组件的环境变量（EventBus 相关的已由 eventBus 写好）。
func (r *renderer) environment(component servicecatalog.Component, values map[string]string) {
	id, layout := component.ID, r.layout
	// 服务日志上报 CLS：只给声明了 cls 凭据的组件。变量名用 MOOX_LOG_CLS_ 前缀，和 Collector 开通采集日志用的 MOOX_CLS_* 分开。
	if cls := r.Config.Logging.CLS; cls.Enabled && component.HasCredential("cls") {
		values["MOOX_LOG_CLS_ENABLED"] = "true"
		values["MOOX_LOG_CLS_ENDPOINT"], values["MOOX_LOG_CLS_TOPIC_ID"] = cls.Endpoint, cls.TopicID
		values["MOOX_LOG_CLS_SECRET_ID"], values["MOOX_LOG_CLS_SECRET_KEY"] = r.CLS.WriterSecretID, r.CLS.WriterSecretKey
		values["MOOX_LOG_CLS_MIN_LEVEL"], values["MOOX_LOG_CLS_SOURCE"] = cls.MinLevel, r.Host.Name
	}
	switch id {
	case "admin":
		values["MOOX_ADMIN_NODE_ID"] = r.Host.Name
		values["MOOX_ADMIN_ENCRYPTION_KEY_FILE"] = layout.Secret("admin-master.key")
		values["MOOX_ADMIN_PKI_DIR"] = layout.Secret("pki")
		// 管理台代浏览器调用各服务时用控制机的主机密钥、以 console 的身份签名。
		values["MOOX_ADMIN_HOST_KEY_FILE"] = layout.Secret("host.key")
		values["MOOX_ADMIN_DB_PATH"] = path.Join(layout.Data(id), "admin.db")
		// 浏览器入口由 Admin 自己监听：证书用 MooX CA 签，Host 必须等于控制机地址。
		values["MOOX_ADMIN_WEB_LISTEN"], values["MOOX_ADMIN_WEB_ADDRESS"] = "0.0.0.0:9527", r.Host.Address
	case "collector":
		// Collector 建函数时开通采集日志主题，保留天数来自 moox.toml 的 [logging] retention_days（只在创建主题时生效）。
		values["MOOX_CLS_RETENTION_DAYS"] = strconv.FormatInt(r.Config.Logging.RetentionDays, 10)
	case "collector-stream":
		values["MOOX_SPACE_ID"] = defaultStreamSpace
	case "storage-primary", "storage-view":
		values["MOOX_STORAGE_ROLE"] = strings.TrimPrefix(id, "storage-")
		values["MOOX_STORAGE_HOME"] = layout.Data(id)
		values["MOOX_STORAGE_CONFIG"] = path.Join(layout.Config(id), "storage.yaml")
		values["MOOX_STORAGE_METADATA_PATH"] = path.Join(layout.Data(id), "metadata/storage_metadata.db")
		values["MOOX_STORAGE_PRIMARY_TARGET"] = "ip://127.0.0.1:20102"
		values["MOOX_STORAGE_METADATA_TARGET"] = "ip://127.0.0.1:20100"
		values["MOOX_STORAGE_VIEW_TARGET"] = "ip://127.0.0.1:20103"
	}
}

// configFile 渲染一个 YAML 配置模板：路径换成新布局的绝对路径，网关身份、EventBus 地址等按这台主机填入。
func (r *renderer) configFile(component servicecatalog.Component, name string, raw []byte) ([]byte, error) {
	id, layout := component.ID, r.layout
	var err error
	if id == "collector" && name == "app.yaml" {
		if raw, err = topology.RenderCollectorRuntimeConfig(r.Config, raw); err != nil {
			return nil, err
		}
	}
	if id == "egress-proxy" && name == "app.yaml" {
		if raw, err = topology.RenderEgressConfig(r.Config, raw); err != nil {
			return nil, err
		}
	}
	document, err := parseYAML(raw)
	if err != nil {
		return nil, err
	}
	set := func(keyPath string, value any) {
		err = errors.Join(err, setPath(document, keyPath, value, true))
	}
	// tRPC 的文件日志写到 logs/<组件>/，用它自带的按大小轮转；标准输出与标准错误交给 journald。
	if writers := logWriters(document); len(writers) > 0 {
		for _, writer := range writers {
			err = errors.Join(err, setPath(writer, "log_path", layout.Logs(id), true))
		}
	}
	if client := member(document.Content[0], "gateway_client"); client != nil {
		if err := r.gatewayClient(component, client); err != nil {
			return nil, err
		}
	}
	url := r.Config.EventBusURLFor(r.Host.Name)
	credential := path.Join(layout.Secret("eventbus"), primaryRole(component)+".yaml")
	data := layout.Data(id)
	// Storage 的两个角色各有自己的数据目录；框架配置里也带一份 storage 段，一并改成绝对路径。
	if (id == "storage-primary" || id == "storage-view") && hasPath(document, "storage") {
		set("storage.root", data)
		set("storage.policy_file", path.Join(layout.Config(id), "storage-policy.json"))
		set("storage.metadata", map[string]string{"path": path.Join(data, "metadata/storage_metadata.db")})
		devices := map[string]string{"pebble_path": path.Join(data, "pebble")}
		if id == "storage-view" {
			devices["view_index_root"] = path.Join(data, "view-indexes")
		}
		set("storage.devices", devices)
		set("storage.eventbus", map[string]string{"credential_file": credential})
	}
	if name == "app.yaml" || name == "engine.yaml" {
		if dbFile, ok := sqliteDatabaseFiles[id]; ok {
			set("database.path", path.Join(data, dbFile))
		}
		if hasPath(document, "eventbus") {
			set("eventbus.urls", []string{url})
			set("eventbus.credential_file", credential)
		}
		switch id {
		case "host-agent":
			set("host_id", r.Host.Name)
			set("host_policy_path", layout.HostPolicy())
			set("gateway_client", map[string]string{})
			if err == nil {
				err = r.gatewayClient(component, member(document.Content[0], "gateway_client"))
			}
		case "eventbus":
			directory := layout.Secret("eventbus")
			set("broker.host", "0.0.0.0")
			set("broker.port", r.Config.EventBus.Port)
			set("broker.client_advertise", r.Config.EventBusAdvertiseAddress())
			set("broker.server_name", "eventbus-"+r.Host.Name)
			set("broker.store_dir", path.Join(data, "jetstream"))
			set("broker.auth", map[string]any{"enabled": true, "users_file": path.Join(directory, "users.yaml")})
			// 服务端证书就是这台主机的主机证书，客户端（含 EventBus 自己的内部连接）用 MooX CA 根证书校验。
			set("broker.tls", map[string]any{"enabled": true, "cert_file": layout.Secret(hostbundle.HostCertificateFile), "key_file": layout.Secret(hostbundle.HostTLSKeyFile), "ca_file": layout.Secret(hostbundle.CAFile)})
			set("internal_client", map[string]string{"credential_file": credential})
		case "collector":
			if r.Identity.SCFCollectorPublicKey == "" {
				return nil, errors.New("身份材料里没有 scf-collector 的公钥")
			}
			set("scf_entry.caller", "scf-collector")
			set("scf_entry.key_file", layout.Secret("scf-collector.key"))
		case "collector-stream":
			set("data_dir", data)
		case "factor-engine":
			set("engine.id", id+"@"+r.Host.Name)
			set("catalog_sync.state_file", path.Join(data, "engine/catalog.json"))
			set("seriesstore.dir", path.Join(data, "seriesstore"))
			set("gap.ledger_dir", path.Join(data, "gap"))
			set("watermark.state_file", path.Join(data, "engine/watermarks.json"))
			set("params.cache_file", path.Join(data, "engine/param_cache.json"))
			// 这两项也要放进数据目录：config/<组件> 每次部署整目录替换，留在那里暂停状态和 UDF 缓存会被删掉。
			set("lifecycle.state_file", path.Join(data, "engine/lifecycle.json"))
			set("udf.dir", path.Join(data, "udf"))
		case "archive-worker":
			set("worker.worker_id", id+"@"+r.Host.Name)
			set("worker.root_dir", path.Join(layout.Root, "archive"))
			if section := r.objectStore(); section != nil {
				set("object_store", section)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return encodeYAML(document)
}

// logWriters 找出 tRPC 配置里写文件的日志写入器的 writer_config。
func logWriters(document *yaml.Node) []*yaml.Node {
	writers := member(member(member(document.Content[0], "plugins"), "log"), "default")
	if writers == nil || writers.Kind != yaml.SequenceNode {
		return nil
	}
	var configs []*yaml.Node
	for _, writer := range writers.Content {
		if kind := member(writer, "writer"); kind != nil && kind.Value == "file" {
			if config := member(writer, "writer_config"); config != nil {
				configs = append(configs, config)
			}
		}
	}
	return configs
}

// gatewayClient 写 gateway_client 段：组件都用本机的主机签名私钥、连本机主机网关。
func (r *renderer) gatewayClient(component servicecatalog.Component, client *yaml.Node) error {
	if r.Identity.SignPublicKey == "" {
		return errors.New("身份材料里没有这台主机的签名公钥")
	}
	for _, field := range [][2]string{
		{"caller", component.ID}, {"key_file", r.layout.Secret("host.key")},
		{"host_id", r.Host.Name}, {"local_address", localGatewayAddress}, {"ca_file", r.layout.Secret("moox-ca.crt")},
	} {
		if err := setPath(client, field[0], field[1], true); err != nil {
			return err
		}
	}
	return nil
}

// objectStore 是归档 worker 的 object_store 段：未配 [archive_object_store] 时返回 nil（省略整段，仅保留本机磁盘归档）。
func (r *renderer) objectStore() map[string]any {
	store := r.Config.ArchiveObjectStore
	if store == nil {
		return nil
	}
	credentials := r.layout.Secret(objectStoreEnvFile)
	section := map[string]any{
		"endpoint": store.Endpoint, "region": store.Region, "bucket": store.Bucket, "url_style": store.URLStyle,
		"use_tls": store.TLS(), "credentials_file": credentials,
	}
	if store.Prefix != "" {
		section["prefix"] = store.Prefix
	}
	if store.StorageClass != "" {
		section["storage_class"] = store.StorageClass
	}
	return section
}

// generated 写不来自模板的文件：主机网关的 app.yaml、存储策略、外部对象存储的凭据。
func (r *renderer) generated(component servicecatalog.Component, environment map[string]string) error {
	id, layout := component.ID, r.layout
	switch id {
	case "host-gateway":
		control := r.Config.ControlHost()
		config := hostgatewayconfig.Default(r.Host.Name, control.Name, control.Address)
		config.TLS = hostgatewayconfig.TLS{CertificateFile: layout.Secret("host.crt"), KeyFile: layout.Secret("host.tls.key"), CAFile: layout.Secret("moox-ca.crt")}
		config.Control.KeyFile = layout.Secret("host.key")
		config.Store.Path = layout.Data(id)
		// 标了外部入口的主机，网关多开 11004。
		if r.Host.ExternalEntry {
			config.Server.ExternalAddr = hostgatewayconfig.ExternalEntryAddr
		}
		raw, err := hostgatewayconfig.Encode(config)
		if err != nil {
			return err
		}
		r.put(id, "config/host-gateway/app.yaml", raw, 0o600)
	case "storage-primary", "storage-view":
		// Storage 的两个角色用同一份策略；它们必须在同一台主机上（内部 RPC 只听回环地址）。
		if r.Config.ComponentHost("storage-primary") != r.Config.ComponentHost("storage-view") {
			return errors.New("storage-primary 与 storage-view 要部署在同一台主机上")
		}
		raw, err := r.Config.StoragePolicy().Encode()
		if err != nil {
			return err
		}
		r.put(id, path.Join("config", id, "storage-policy.json"), raw, 0o600)
	case "archive-worker":
		if len(r.ObjectStoreEnv) > 0 {
			r.put(id, "secrets/"+objectStoreEnvFile, bytes.Clone(r.ObjectStoreEnv), 0o600)
		} else if r.Config.ArchiveObjectStore != nil {
			return errors.New("配了 [archive_object_store]，但没有取到它的密钥")
		}
	}
	return nil
}

type unitValues struct {
	Component, Description, After, WorkingDirectory, EnvironmentFile, ExecStart string
	StopTimeoutSec                                                              int
}

func (r *renderer) unit(component servicecatalog.Component, unitTemplate *template.Template) ([]byte, error) {
	id := component.ID
	after := []string{}
	for _, dependency := range component.After {
		if slices.Contains(r.HostComponents, dependency) {
			after = append(after, UnitName(dependency))
		}
	}
	command := []string{r.layout.Bin(component.Binary)}
	for _, argument := range component.Args {
		command = append(command, r.layout.Expand(id, argument))
	}
	values := unitValues{
		Component: id, Description: "MooX " + id, After: strings.Join(after, " "),
		WorkingDirectory: r.layout.Data(id), EnvironmentFile: r.layout.Env(id), ExecStart: strings.Join(command, " "),
		StopTimeoutSec: component.StopTimeout(),
	}
	for _, value := range []string{values.After, values.WorkingDirectory, values.EnvironmentFile, values.ExecStart} {
		if strings.ContainsAny(value, "\r\n%\"'\\$") {
			return nil, errors.New("单元文件里的路径或参数含有 systemd 要转义的字符（部署根只用普通字符）")
		}
	}
	var rendered bytes.Buffer
	if err := unitTemplate.Execute(&rendered, values); err != nil {
		return nil, fmt.Errorf("渲染单元模板: %w", err)
	}
	return rendered.Bytes(), nil
}

// encodeEnvironment 写 systemd 的 EnvironmentFile：每行 KEY="值"，按键排序。
func encodeEnvironment(values map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output bytes.Buffer
	for _, key := range keys {
		value := values[key]
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("环境变量 %s 的值含有换行或空字符", key)
		}
		value = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
		fmt.Fprintf(&output, "%s=\"%s\"\n", key, value)
	}
	return output.Bytes(), nil
}

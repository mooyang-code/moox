package accessproxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"
)

// Principal is one caller Storage Access admits: the gateway credential it
// signs inbound requests with, the credential Access re-signs upstream
// requests with, and the Storage methods it may call.
type Principal struct {
	Name     string
	Inbound  gatewayauth.Credentials
	Upstream gatewayauth.Credentials
	Methods  map[string]map[string]struct{}
}

func (p Principal) allows(servicePath, method string) bool {
	_, ok := p.Methods[servicePath][method]
	return ok
}

// MethodPresets are the only method sets a principal can be given; allowlists
// live in code so a configuration file can never widen them.
var MethodPresets = map[string]map[string][]string{
	// collector serves SCF market fetchers and resamplers.
	"collector": {
		PrimaryStoreName: {
			"UpsertFields", "ReadFields", "ReadTimeSeriesRows", "ReadRecordRows",
			"ReportCollectorPeriodCompleted", "WaitViewSyncPoint",
			"EnsureDatasetPeriod", "CommitTimeSeriesBatch",
			"RecordDatasetPeriodFailures", "GetDatasetPeriodStatus",
		},
		MetadataName: {
			"GetSubject", "ListSubjects",
			"UpsertTag", "GetTag", "ListTags", "DeleteTag", "ListTagMembers",
			"AddTagMembers", "RemoveTagMembers", "SetTagMemberStatus", "ApplyTagSnapshot",
			"ReportTagRunFailure", "UpdateSubjectAttributes", "ResolveSubjects",
			"GetDataset", "ListDatasets", "CreateDataset", "UpdateDataset", "DeleteDataset",
			"CheckDatasetActivation", "ActivateDataset", "ListDatasetSubjects",
			"UpsertSubject", "UpsertDatasetColumn", "ListDatasetColumns",
			"CreateView", "GetView", "ListViews", "UpdateView", "DeleteView",
			"UpsertViewColumn", "ListViewColumns", "RequestViewRebuild",
		},
		DataViewName: {"QueryTimeSeriesRows", "SearchRecordRows"},
	},
	// factor-engine reads source windows and writes factor results; dataset
	// lifecycle stays with moox-factor-mgr on the control host.
	"factor-engine": {
		PrimaryStoreName: {"ReadTimeSeriesRows", "WriteFactorRows", "ReportFactorPeriodComputed", "GetFactorPeriodComputed"},
		MetadataName:     {"GetDataset", "ListDatasetColumns", "ListDatasetSubjects"},
	},
}

func presetMethods(name string) (map[string]map[string]struct{}, error) {
	preset, ok := MethodPresets[name]
	if !ok {
		names := make([]string, 0, len(MethodPresets))
		for known := range MethodPresets {
			names = append(names, known)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown method preset %q (known: %s)", name, strings.Join(names, ", "))
	}
	methods := make(map[string]map[string]struct{}, len(preset))
	for service, names := range preset {
		methods[service] = make(map[string]struct{}, len(names))
		for _, method := range names {
			methods[service][method] = struct{}{}
		}
	}
	return methods, nil
}

type principalsFile struct {
	Version    int                  `yaml:"version"`
	Principals []principalFileEntry `yaml:"principals"`
}

type principalFileEntry struct {
	Name     string         `yaml:"name"`
	Methods  string         `yaml:"methods"`
	Inbound  credentialFile `yaml:"inbound"`
	Upstream credentialFile `yaml:"upstream"`
}

type credentialFile struct {
	KeyID      string `yaml:"key_id"`
	Caller     string `yaml:"caller"`
	SecretFile string `yaml:"secret_file"`
}

// LoadPrincipals reads the principals file. Secret files resolve against the
// file's directory and must be regular 0600 files.
func LoadPrincipals(path string) ([]Principal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read storage access principals: %w", err)
	}
	var file principalsFile
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse storage access principals %s: %w", path, err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("storage access principals version must be 1")
	}
	base := filepath.Dir(path)
	principals := make([]Principal, 0, len(file.Principals))
	for _, entry := range file.Principals {
		methods, err := presetMethods(strings.TrimSpace(entry.Methods))
		if err != nil {
			return nil, fmt.Errorf("principal %q: %w", entry.Name, err)
		}
		inbound, err := entry.Inbound.load(base)
		if err != nil {
			return nil, fmt.Errorf("principal %q inbound: %w", entry.Name, err)
		}
		upstream, err := entry.Upstream.load(base)
		if err != nil {
			return nil, fmt.Errorf("principal %q upstream: %w", entry.Name, err)
		}
		principals = append(principals, Principal{Name: strings.TrimSpace(entry.Name), Inbound: inbound, Upstream: upstream, Methods: methods})
	}
	if len(principals) == 0 {
		return nil, errors.New("storage access principals file declares no principal")
	}
	return principals, nil
}

func (c credentialFile) load(base string) (gatewayauth.Credentials, error) {
	path := strings.TrimSpace(c.SecretFile)
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	credentials, err := gatewayauth.CredentialsFromKeyFile(c.KeyID, path)
	if err != nil {
		return gatewayauth.Credentials{}, err
	}
	credentials.Caller = strings.TrimSpace(c.Caller)
	return credentials, nil
}

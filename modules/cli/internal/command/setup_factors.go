package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	adminclient "github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

var factorIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// setupFactorSet is one factor set to create; sets are keyed by SetID.
type setupFactorSet struct {
	SetID           string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	SubjectMode     string
	Subjects        []string
}

// setupFactorDefinition is the global factor contract. It carries no set or
// status: those belong to a membership.
type setupFactorDefinition struct {
	FactorType      string
	FactorID        string
	Name            string
	SourceCode      string
	SourceHash      string
	InputColumns    []string
	Outputs         []string
	ParamsJSON      string
	LookbackPeriods int
}

type setupFactorMember struct {
	SetID    string
	FactorID string
	Status   string
}

// setupFactorPlan is what a setup run applies: sets first, then definitions,
// then memberships.
type setupFactorPlan struct {
	Sets        []setupFactorSet
	Definitions []setupFactorDefinition
	Members     []setupFactorMember
}

func (p setupFactorPlan) empty() bool {
	return len(p.Sets) == 0 && len(p.Definitions) == 0 && len(p.Members) == 0
}

type setupFactorSummary struct {
	Enabled          bool `json:"enabled"`
	Definitions      int  `json:"definitions"`
	Members          int  `json:"members"`
	SetsCreated      int  `json:"sets_created"`
	Imported         int  `json:"imported"`
	Unchanged        int  `json:"unchanged"`
	MembersAdded     int  `json:"members_added"`
	MembersEnabled   int  `json:"members_enabled"`
	MembersUnchanged int  `json:"members_unchanged"`
}

type setupInitFactor interface {
	Apply(context.Context, setupFactorPlan) (setupFactorSummary, error)
	Close() error
}

// These defaults define the initial factor definitions and set memberships for
// a fresh installation.
func defaultSetupFactorDefinitions() []setupconfig.FactorSetupDefinition {
	return []setupconfig.FactorSetupDefinition{
		{
			FactorType: "timeseries", FactorID: "Bias", File: "Bias.py", Name: "Bias",
			InputColumns: []string{"close"}, Outputs: []string{"bias_20"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
		},
		{
			FactorType: "timeseries", FactorID: "Cci", File: "Cci.py", Name: "Cci",
			InputColumns: []string{"high", "low", "close"}, Outputs: []string{"cci"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
		},
		{
			FactorType: "timeseries", FactorID: "MinMax", File: "MinMax.py", Name: "MinMax",
			InputColumns: []string{"high", "low", "close"}, Outputs: []string{"minmax_20"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
		},
		{
			FactorType: "timeseries", FactorID: "QuoteVolumeMean", File: "QuoteVolumeMean.py", Name: "QuoteVolumeMean",
			InputColumns: []string{"quote_volume"}, Outputs: []string{"quote_volume_mean_20"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
		},
	}
}

func defaultSetupFactorMembers(definitions []setupconfig.FactorSetupDefinition) []setupconfig.FactorSetupMember {
	members := make([]setupconfig.FactorSetupMember, 0, len(definitions))
	for _, def := range definitions {
		members = append(members, setupconfig.FactorSetupMember{
			SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", FactorID: def.FactorID, Status: "enabled",
		})
	}
	return members
}

func loadSetupFactors(manifest setupconfig.Manifest, repoRoot string) (setupFactorPlan, error) {
	var plan setupFactorPlan
	if !manifest.Factors.Enabled {
		return plan, nil
	}
	if strings.TrimSpace(manifest.Factors.SourceDir) == "" {
		manifest.Factors.SourceDir = "modules/factor/factors"
	}
	definitions, members := manifest.Factors.Definitions, manifest.Factors.Members
	if len(definitions) == 0 && len(members) == 0 && len(manifest.Factors.Sets) == 0 {
		defaultRoot := filepath.Join(repoRoot, filepath.FromSlash(manifest.Factors.SourceDir))
		if _, err := os.Stat(defaultRoot); err != nil {
			if os.IsNotExist(err) {
				return plan, nil
			}
			return plan, fmt.Errorf("stat factors directory: %w", err)
		}
		definitions = defaultSetupFactorDefinitions()
		members = defaultSetupFactorMembers(definitions)
		manifest.Factors.Sets = []setupconfig.FactorSetupSet{{
			SpaceID: "crypto", SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", SubjectMode: "all",
		}}
	} else if len(definitions) == 0 {
		return plan, fmt.Errorf("factors.definitions must be configured when factors.sets or factors.members is specified")
	}
	sets := make(map[string]setupconfig.FactorSetupSet, len(manifest.Factors.Sets))
	for _, set := range manifest.Factors.Sets {
		key := factorSetupIdentity(set.SourceDatasetID, set.Freq)
		if key == "\x00" {
			return plan, fmt.Errorf("factor set requires source_dataset_id and freq")
		}
		if _, exists := sets[key]; exists {
			return plan, fmt.Errorf("factor set for source_dataset_id %q and freq %q is duplicated", set.SourceDatasetID, set.Freq)
		}
		sets[key] = set
		plan.Sets = append(plan.Sets, setupFactorSet{
			SetID: factorSetID(set.SourceDatasetID, set.Freq), SpaceID: strings.TrimSpace(set.SpaceID),
			SourceDatasetID: strings.TrimSpace(set.SourceDatasetID), Freq: strings.TrimSpace(set.Freq),
			SubjectMode: defaultString(strings.TrimSpace(set.SubjectMode), "all"), Subjects: cleanStrings(set.Subjects),
		})
	}
	sort.Slice(plan.Sets, func(i, j int) bool { return plan.Sets[i].SetID < plan.Sets[j].SetID })
	root := filepath.Join(repoRoot, filepath.FromSlash(manifest.Factors.SourceDir))
	seen := make(map[string]struct{}, len(definitions))
	for index, item := range definitions {
		if item.FactorType != "timeseries" && item.FactorType != "cross_section" {
			return plan, fmt.Errorf("factors.definitions[%d].factor_type must be timeseries or cross_section", index)
		}
		factorID := strings.TrimSpace(item.FactorID)
		if !factorIDPattern.MatchString(factorID) {
			return plan, fmt.Errorf("factors.definitions[%d].factor_id is invalid", index)
		}
		if _, exists := seen[factorID]; exists {
			return plan, fmt.Errorf("factor %q is configured more than once", factorID)
		}
		seen[factorID] = struct{}{}
		path := filepath.Join(root, filepath.FromSlash(item.File))
		resolvedRoot, err := filepath.Abs(root)
		if err != nil {
			return plan, fmt.Errorf("resolve factors directory: %w", err)
		}
		resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
		if err != nil {
			return plan, fmt.Errorf("resolve factors directory: %w", err)
		}
		resolvedPath, err := filepath.Abs(path)
		if err != nil {
			return plan, fmt.Errorf("resolve factor %q path: %w", factorID, err)
		}
		resolvedPath, err = filepath.EvalSymlinks(resolvedPath)
		if err != nil || !pathWithin(resolvedRoot, resolvedPath) {
			return plan, fmt.Errorf("factor %q file must stay under factors.source_dir", factorID)
		}
		source, err := os.ReadFile(resolvedPath)
		if err != nil {
			return plan, fmt.Errorf("read factor %q source: %w", factorID, err)
		}
		sourceCode := strings.TrimSpace(string(source))
		factorName := strings.TrimSuffix(filepath.Base(resolvedPath), filepath.Ext(resolvedPath))
		if factorID != factorName {
			return plan, fmt.Errorf("factor_id %q must match factor file name %q", factorID, factorName)
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = factorName
		}
		if name != factorName {
			return plan, fmt.Errorf("factor %q name must match factor file name %q", factorID, factorName)
		}
		if !factorIDPattern.MatchString(name) {
			return plan, fmt.Errorf("factor %q name is invalid", factorID)
		}
		params := strings.TrimSpace(item.ParamsJSON)
		if params == "" {
			params = "{}"
		}
		var paramsValue map[string]any
		if err := json.Unmarshal([]byte(params), &paramsValue); err != nil || paramsValue == nil {
			return plan, fmt.Errorf("factor %q params_json must be a JSON object", factorID)
		}
		inputColumns := cleanStrings(item.InputColumns)
		outputs := cleanStrings(item.Outputs)
		sort.Strings(inputColumns)
		sort.Strings(outputs)
		if len(inputColumns) == 0 || len(outputs) == 0 {
			return plan, fmt.Errorf("factor %q must declare input_columns and outputs", factorID)
		}
		hash := sha256.Sum256([]byte(sourceCode))
		plan.Definitions = append(plan.Definitions, setupFactorDefinition{
			FactorType: item.FactorType,
			FactorID:   factorID, Name: name, SourceCode: sourceCode, SourceHash: "sha256:" + hex.EncodeToString(hash[:]),
			InputColumns: inputColumns, Outputs: outputs, ParamsJSON: params, LookbackPeriods: item.LookbackPeriods,
		})
	}
	sort.Slice(plan.Definitions, func(i, j int) bool { return plan.Definitions[i].FactorID < plan.Definitions[j].FactorID })
	memberSeen := make(map[string]struct{}, len(members))
	for index, member := range members {
		datasetID, freq := strings.TrimSpace(member.SourceDatasetID), strings.TrimSpace(member.Freq)
		if _, ok := sets[factorSetupIdentity(datasetID, freq)]; !ok {
			return plan, fmt.Errorf("factors.members[%d] has no matching set for source_dataset_id %q and freq %q", index, datasetID, freq)
		}
		factorID := strings.TrimSpace(member.FactorID)
		if _, ok := seen[factorID]; !ok {
			return plan, fmt.Errorf("factors.members[%d] references unknown factor_id %q", index, factorID)
		}
		status := defaultString(strings.TrimSpace(member.Status), "enabled")
		if status != "enabled" && status != "disabled" {
			return plan, fmt.Errorf("factors.members[%d].status must be enabled or disabled", index)
		}
		setID := factorSetID(datasetID, freq)
		key := setID + "\x00" + factorID
		if _, dup := memberSeen[key]; dup {
			return plan, fmt.Errorf("factor %q is a member of set %q more than once", factorID, setID)
		}
		memberSeen[key] = struct{}{}
		plan.Members = append(plan.Members, setupFactorMember{SetID: setID, FactorID: factorID, Status: status})
	}
	sort.Slice(plan.Members, func(i, j int) bool {
		if plan.Members[i].SetID != plan.Members[j].SetID {
			return plan.Members[i].SetID < plan.Members[j].SetID
		}
		return plan.Members[i].FactorID < plan.Members[j].FactorID
	})
	return plan, nil
}

func factorSetupIdentity(sourceDatasetID, freq string) string {
	return strings.TrimSpace(sourceDatasetID) + "\x00" + strings.TrimSpace(freq)
}

func factorSetID(sourceDatasetID, freq string) string {
	suffix := strings.TrimPrefix(strings.TrimSpace(sourceDatasetID), "dataset_")
	freq = strings.TrimSpace(freq)
	if !strings.HasSuffix(suffix, "_"+freq) {
		suffix += "_" + freq
	}
	return "fset_" + suffix
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

type remoteSetupFactor struct {
	transport        setupssh.Client
	listener         net.Listener
	client           factorJSONClient
	fallbackListener net.Listener
	fallback         factorJSONClient
}

type factorJSONClient interface {
	CallJSON(context.Context, string, string, any, any) error
}

func defaultOpenSetupFactor(ctx context.Context, snapshot *setupconfig.Snapshot) (setupInitFactor, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("factor_setup_invalid")
	}
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return nil, err
	}
	listener, err := transport.ForwardLocal(ctx, "127.0.0.1:11002")
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("factor_gateway_unavailable")
	}
	controlRoot := snapshot.Manifest.Paths.Resolved().ControlRoot
	secretRaw, err := readRemoteControlFile(ctx, transport, filepath.Join(controlRoot, "secrets/gateway-moox-cli.key"))
	if err != nil {
		_ = listener.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("factor_gateway_credentials_unavailable")
	}
	envRaw, err := readRemoteControlFile(ctx, transport, filepath.Join(controlRoot, "secrets/gateway-service.env"))
	if err != nil {
		_ = listener.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("factor_gateway_credentials_unavailable")
	}
	nodeID := envValue(string(envRaw), "MOOX_GATEWAY_NODE_ID")
	if nodeID == "" {
		_ = listener.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("factor_gateway_credentials_unavailable")
	}
	client := adminclient.New("http://" + listener.Addr().String())
	client.ServiceAuth = &adminclient.ServiceAuthConfig{
		AccessKey: "moox-cli", SecretKey: strings.TrimSpace(string(secretRaw)), Caller: "moox-cli", TargetNode: nodeID, ExpireSecs: 60,
	}
	// A stale gateway route cache can still point at the old tRPC port while
	// Factor's HTTP service is healthy. Keep a loopback-only SSH fallback so a
	// setup run can repair definitions without weakening the normal gateway
	// authentication path. The fallback is never exposed outside the SSH
	// tunnel and is only used for a gateway 502.
	var fallbackListener net.Listener
	var fallback factorJSONClient
	if direct, directErr := transport.ForwardLocal(ctx, "127.0.0.1:11404"); directErr == nil {
		fallbackListener = direct
		fallback = adminclient.New("http://" + direct.Addr().String())
	}
	return &remoteSetupFactor{transport: transport, listener: listener, client: client, fallbackListener: fallbackListener, fallback: fallback}, nil
}

func envValue(raw, key string) string {
	for _, line := range strings.Split(raw, "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && name == key {
			return strings.Trim(strings.TrimSpace(value), "\"'")
		}
	}
	return ""
}

type factorAPIRetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// factorAPIDef mirrors the FactorDef proto JSON: a definition has no set or
// status, those live in the usages beside it.
type factorAPIDef struct {
	FactorType      string   `json:"factor_type"`
	FactorID        string   `json:"factor_id"`
	Name            string   `json:"name"`
	SourceCode      string   `json:"source_code"`
	SourceHash      string   `json:"source_hash"`
	InputColumns    []string `json:"input_columns"`
	Outputs         []string `json:"outputs"`
	ParamsJSON      string   `json:"params_json"`
	LookbackPeriods int      `json:"lookback_periods"`
}

// factorAPIUsage mirrors the FactorUsage proto JSON.
type factorAPIUsage struct {
	SetID  string `json:"set_id"`
	Status string `json:"status"`
}

// factorAPIInfo mirrors the FactorInfo proto JSON (ListFactors element).
type factorAPIInfo struct {
	Factor factorAPIDef     `json:"factor"`
	Usages []factorAPIUsage `json:"usages"`
}

type factorAPIResponse struct {
	RetInfo   factorAPIRetInfo `json:"ret_info"`
	Factor    factorAPIDef     `json:"factor"`
	Usages    []factorAPIUsage `json:"usages"`
	Factors   []factorAPIInfo  `json:"factors"`
	FactorSet struct {
		SetID           string   `json:"set_id"`
		SpaceID         string   `json:"space_id"`
		SourceDatasetID string   `json:"source_dataset_id"`
		Freq            string   `json:"freq"`
		SubjectMode     string   `json:"subject_mode"`
		Subjects        []string `json:"subjects"`
		Status          string   `json:"status"`
	} `json:"factor_set"`
}

func (r factorAPIResponse) err(method string) error {
	if r.RetInfo.Code == 0 || r.RetInfo.Code == 200 {
		return nil
	}
	return fmt.Errorf("FactorMgr %s failed (%d): %s", method, r.RetInfo.Code, r.RetInfo.Msg)
}

func (r *remoteSetupFactor) call(ctx context.Context, method string, body any, response *factorAPIResponse) error {
	path := "/api/admin/factormgr/" + method
	if err := r.client.CallJSON(ctx, http.MethodPost, path, body, response); err != nil {
		if r.fallback == nil || !strings.Contains(err.Error(), "HTTP 502") {
			return err
		}
		if fallbackErr := r.fallback.CallJSON(ctx, http.MethodPost, "/trpc.moox.factor.FactorMgr/"+method, body, response); fallbackErr != nil {
			return fallbackErr
		}
		return response.err(method)
	}
	return response.err(method)
}

// Apply is idempotent and additive: it creates what is missing and never
// prunes definitions or members that moox.toml does not mention.
func (r *remoteSetupFactor) Apply(ctx context.Context, plan setupFactorPlan) (setupFactorSummary, error) {
	summary := setupFactorSummary{Enabled: !plan.empty(), Definitions: len(plan.Definitions), Members: len(plan.Members)}
	for _, set := range plan.Sets {
		get := factorAPIResponse{}
		err := r.call(ctx, "GetFactorSet", map[string]any{"set_id": set.SetID}, &get)
		if err != nil && !factorAPINotFound(get) {
			return summary, err
		}
		if err != nil {
			create := factorAPIResponse{}
			if createErr := r.call(ctx, "CreateFactorSet", map[string]any{"factor_set": map[string]any{
				"set_id": set.SetID, "space_id": set.SpaceID, "source_dataset_id": set.SourceDatasetID,
				"freq": set.Freq, "subject_mode": set.SubjectMode, "subjects": set.Subjects, "status": "pending",
			}}, &create); createErr != nil {
				return summary, createErr
			}
			summary.SetsCreated++
			continue
		}
		if get.FactorSet.SpaceID != set.SpaceID || get.FactorSet.SourceDatasetID != set.SourceDatasetID || get.FactorSet.Freq != set.Freq {
			return summary, fmt.Errorf("factor set %q already exists with a different source identity", set.SetID)
		}
		if get.FactorSet.SubjectMode != set.SubjectMode || !slices.Equal(cleanStrings(get.FactorSet.Subjects), set.Subjects) {
			updated := factorAPIResponse{}
			if updateErr := r.call(ctx, "UpdateFactorSet", map[string]any{
				"set_id": set.SetID, "subject_mode": set.SubjectMode, "subjects": set.Subjects,
			}, &updated); updateErr != nil {
				return summary, updateErr
			}
		}
	}
	// usages[factor_id][set_id] is the current membership status, kept in step
	// with the calls below so one GetFactor per definition is enough.
	usages := make(map[string]map[string]string, len(plan.Definitions))
	for _, def := range plan.Definitions {
		get := factorAPIResponse{}
		err := r.call(ctx, "GetFactor", map[string]any{"factor_id": def.FactorID}, &get)
		if err != nil && !factorAPINotFound(get) {
			return summary, err
		}
		usages[def.FactorID] = map[string]string{}
		if err != nil {
			create := factorAPIResponse{}
			if createErr := r.call(ctx, "CreateFactor", map[string]any{"factor": map[string]any{
				"factor_type": def.FactorType, "factor_id": def.FactorID,
				"name": def.Name, "source_code": def.SourceCode, "source_hash": def.SourceHash,
				"input_columns": def.InputColumns, "outputs": def.Outputs, "params_json": def.ParamsJSON,
				"lookback_periods": def.LookbackPeriods,
			}}, &create); createErr != nil {
				return summary, createErr
			}
			summary.Imported++
			continue
		}
		if !sameFactorContract(get.Factor, def) {
			return summary, fmt.Errorf("factor %q already exists with a different definition; update moox.toml or replace it explicitly", def.FactorID)
		}
		for _, usage := range get.Usages {
			usages[def.FactorID][usage.SetID] = usage.Status
		}
		summary.Unchanged++
	}
	for _, member := range plan.Members {
		if _, known := usages[member.FactorID]; !known {
			return summary, fmt.Errorf("member references factor %q that is not defined", member.FactorID)
		}
		if _, exists := usages[member.FactorID][member.SetID]; exists {
			continue
		}
		added := factorAPIResponse{}
		if err := r.call(ctx, "AddFactorToSet", map[string]any{"set_id": member.SetID, "factor_id": member.FactorID}, &added); err != nil {
			return summary, err
		}
		usages[member.FactorID][member.SetID] = "disabled"
		summary.MembersAdded++
	}
	for _, member := range plan.Members {
		if usages[member.FactorID][member.SetID] == member.Status {
			summary.MembersUnchanged++
			continue
		}
		status := factorAPIResponse{}
		if err := r.call(ctx, "SetFactorMemberStatus", map[string]any{
			"set_id": member.SetID, "factor_id": member.FactorID, "status": member.Status,
		}, &status); err != nil {
			return summary, err
		}
		usages[member.FactorID][member.SetID] = member.Status
		summary.MembersEnabled++
	}
	return summary, nil
}

func factorAPINotFound(response factorAPIResponse) bool {
	return response.RetInfo.Code == 9 || response.RetInfo.Code == 5 ||
		(response.RetInfo.Code == 4 && strings.Contains(strings.ToLower(response.RetInfo.Msg), "not found"))
}

func sameFactorContract(got factorAPIDef, want setupFactorDefinition) bool {
	if got.FactorType != want.FactorType {
		return false
	}
	if got.SourceHash != want.SourceHash || got.Name != want.Name || got.LookbackPeriods != want.LookbackPeriods || !slicesEqual(got.InputColumns, want.InputColumns) || !slicesEqual(got.Outputs, want.Outputs) {
		return false
	}
	return canonicalJSON(got.ParamsJSON) == canonicalJSON(want.ParamsJSON)
}

func slicesEqual(left, right []string) bool {
	left = cleanStrings(left)
	right = cleanStrings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func canonicalJSON(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &value); err != nil {
		return strings.TrimSpace(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return string(encoded)
}

func (r *remoteSetupFactor) Close() error {
	if r == nil {
		return nil
	}
	if r.listener != nil {
		_ = r.listener.Close()
	}
	if r.fallbackListener != nil {
		_ = r.fallbackListener.Close()
	}
	if r.transport != nil {
		return r.transport.Close()
	}
	return nil
}

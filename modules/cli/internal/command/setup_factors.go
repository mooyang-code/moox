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

type setupFactorItem struct {
	FactorType      string
	FactorID        string
	Name            string
	SourceCode      string
	SourceHash      string
	InputColumns    []string
	Outputs         []string
	ParamsJSON      string
	LookbackPeriods int
	SetID           string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	SubjectMode     string
	Subjects        []string
	Status          string
}

type setupFactorSummary struct {
	Enabled     bool `json:"enabled"`
	Planned     int  `json:"planned"`
	SetsCreated int  `json:"sets_created"`
	Imported    int  `json:"imported"`
	Associated  int  `json:"associated"`
	Unchanged   int  `json:"unchanged"`
}

type setupInitFactor interface {
	Apply(context.Context, []setupFactorItem) (setupFactorSummary, error)
	Close() error
}

// These defaults define the initial factor set for a fresh installation.
func defaultSetupFactorItems() []setupconfig.FactorSetupItem {
	return []setupconfig.FactorSetupItem{
		{
			FactorType: "timeseries", FactorID: "Bias", File: "Bias.py", Name: "Bias",
			InputColumns: []string{"close"}, Outputs: []string{"bias_20"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
			SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", Status: "enabled",
		},
		{
			FactorType: "timeseries", FactorID: "Cci", File: "Cci.py", Name: "Cci",
			InputColumns: []string{"high", "low", "close"}, Outputs: []string{"cci"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
			SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", Status: "enabled",
		},
		{
			FactorType: "timeseries", FactorID: "MinMax", File: "MinMax.py", Name: "MinMax",
			InputColumns: []string{"high", "low", "close"}, Outputs: []string{"minmax_20"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
			SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", Status: "enabled",
		},
		{
			FactorType: "timeseries", FactorID: "QuoteVolumeMean", File: "QuoteVolumeMean.py", Name: "QuoteVolumeMean",
			InputColumns: []string{"quote_volume"}, Outputs: []string{"quote_volume_mean_20"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
			SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", Status: "enabled",
		},
	}
}

func loadSetupFactors(manifest setupconfig.Manifest, repoRoot string) ([]setupFactorItem, error) {
	if !manifest.Factors.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(manifest.Factors.SourceDir) == "" {
		manifest.Factors.SourceDir = "modules/factor/factors"
	}
	items := manifest.Factors.Items
	if len(items) == 0 && len(manifest.Factors.Sets) == 0 {
		defaultRoot := filepath.Join(repoRoot, filepath.FromSlash(manifest.Factors.SourceDir))
		if _, err := os.Stat(defaultRoot); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("stat factors directory: %w", err)
		}
		items = defaultSetupFactorItems()
		manifest.Factors.Sets = []setupconfig.FactorSetupSet{{
			SpaceID: "crypto", SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", SubjectMode: "all",
		}}
	} else if len(items) == 0 {
		return nil, fmt.Errorf("factors.items must be configured when factors.sets is specified")
	}
	sets := make(map[string]setupconfig.FactorSetupSet, len(manifest.Factors.Sets))
	for _, set := range manifest.Factors.Sets {
		key := factorSetupIdentity(set.SourceDatasetID, set.Freq)
		if key == "\x00" {
			return nil, fmt.Errorf("factor set requires source_dataset_id and freq")
		}
		if _, exists := sets[key]; exists {
			return nil, fmt.Errorf("factor set for source_dataset_id %q and freq %q is duplicated", set.SourceDatasetID, set.Freq)
		}
		sets[key] = set
	}
	root := filepath.Join(repoRoot, filepath.FromSlash(manifest.Factors.SourceDir))
	result := make([]setupFactorItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		if item.FactorType != "timeseries" && item.FactorType != "cross_section" {
			return nil, fmt.Errorf("factors.items[%d].factor_type must be timeseries or cross_section", index)
		}
		factorID := strings.TrimSpace(item.FactorID)
		if !factorIDPattern.MatchString(factorID) {
			return nil, fmt.Errorf("factors.items[%d].factor_id is invalid", index)
		}
		if _, exists := seen[factorID]; exists {
			return nil, fmt.Errorf("factor %q is configured more than once", factorID)
		}
		seen[factorID] = struct{}{}
		path := filepath.Join(root, filepath.FromSlash(item.File))
		resolvedRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve factors directory: %w", err)
		}
		resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve factors directory: %w", err)
		}
		resolvedPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve factor %q path: %w", factorID, err)
		}
		resolvedPath, err = filepath.EvalSymlinks(resolvedPath)
		if err != nil || !pathWithin(resolvedRoot, resolvedPath) {
			return nil, fmt.Errorf("factor %q file must stay under factors.source_dir", factorID)
		}
		source, err := os.ReadFile(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("read factor %q source: %w", factorID, err)
		}
		sourceCode := strings.TrimSpace(string(source))
		factorName := strings.TrimSuffix(filepath.Base(resolvedPath), filepath.Ext(resolvedPath))
		if factorID != factorName {
			return nil, fmt.Errorf("factor_id %q must match factor file name %q", factorID, factorName)
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = factorName
		}
		if name != factorName {
			return nil, fmt.Errorf("factor %q name must match factor file name %q", factorID, factorName)
		}
		if !factorIDPattern.MatchString(name) {
			return nil, fmt.Errorf("factor %q name is invalid", factorID)
		}
		params := strings.TrimSpace(item.ParamsJSON)
		if params == "" {
			params = "{}"
		}
		var paramsValue map[string]any
		if err := json.Unmarshal([]byte(params), &paramsValue); err != nil || paramsValue == nil {
			return nil, fmt.Errorf("factor %q params_json must be a JSON object", factorID)
		}
		inputColumns := cleanStrings(item.InputColumns)
		outputs := cleanStrings(item.Outputs)
		sort.Strings(inputColumns)
		sort.Strings(outputs)
		if len(inputColumns) == 0 || len(outputs) == 0 {
			return nil, fmt.Errorf("factor %q must declare input_columns and outputs", factorID)
		}
		status := strings.TrimSpace(item.Status)
		if status == "" {
			status = "enabled"
		}
		datasetID, freq := strings.TrimSpace(item.SourceDatasetID), strings.TrimSpace(item.Freq)
		set, ok := sets[factorSetupIdentity(datasetID, freq)]
		if !ok {
			return nil, fmt.Errorf("factor %q has no matching set for source_dataset_id %q and freq %q", factorID, datasetID, freq)
		}
		setID := factorSetID(datasetID, freq)
		hash := sha256.Sum256([]byte(sourceCode))
		result = append(result, setupFactorItem{
			FactorType: item.FactorType,
			FactorID:   factorID, Name: name, SourceCode: sourceCode, SourceHash: "sha256:" + hex.EncodeToString(hash[:]),
			InputColumns: inputColumns, Outputs: outputs, ParamsJSON: params, LookbackPeriods: item.LookbackPeriods,
			SetID: setID, SpaceID: strings.TrimSpace(set.SpaceID), SourceDatasetID: datasetID, Freq: freq,
			SubjectMode: defaultString(strings.TrimSpace(set.SubjectMode), "all"), Subjects: cleanStrings(set.Subjects), Status: status,
		})
	}
	return result, nil
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

type factorAPIResponse struct {
	RetInfo factorAPIRetInfo `json:"ret_info"`
	Factor  struct {
		FactorType      string   `json:"factor_type"`
		FactorID        string   `json:"factor_id"`
		SetID           string   `json:"set_id"`
		Name            string   `json:"name"`
		SourceCode      string   `json:"source_code"`
		SourceHash      string   `json:"source_hash"`
		InputColumns    []string `json:"input_columns"`
		Outputs         []string `json:"outputs"`
		ParamsJSON      string   `json:"params_json"`
		LookbackPeriods int      `json:"lookback_periods"`
		Status          string   `json:"status"`
	} `json:"factor"`
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

func (r *remoteSetupFactor) Apply(ctx context.Context, items []setupFactorItem) (setupFactorSummary, error) {
	summary := setupFactorSummary{Enabled: len(items) > 0, Planned: len(items)}
	sets := make(map[string]setupFactorItem, len(items))
	for _, item := range items {
		if previous, ok := sets[item.SetID]; ok &&
			(previous.SpaceID != item.SpaceID || previous.SourceDatasetID != item.SourceDatasetID || previous.Freq != item.Freq || previous.SubjectMode != item.SubjectMode || !slices.Equal(previous.Subjects, item.Subjects)) {
			return summary, fmt.Errorf("factor set %q has conflicting configuration", item.SetID)
		}
		sets[item.SetID] = item
	}
	setIDs := make([]string, 0, len(sets))
	for setID := range sets {
		setIDs = append(setIDs, setID)
	}
	sort.Strings(setIDs)
	for _, setID := range setIDs {
		item := sets[setID]
		get := factorAPIResponse{}
		err := r.call(ctx, "GetFactorSet", map[string]any{"set_id": setID}, &get)
		if err != nil && !factorAPINotFound(get) {
			return summary, err
		}
		if err != nil {
			create := factorAPIResponse{}
			if createErr := r.call(ctx, "CreateFactorSet", map[string]any{"factor_set": map[string]any{
				"set_id": setID, "space_id": item.SpaceID, "source_dataset_id": item.SourceDatasetID,
				"freq": item.Freq, "subject_mode": item.SubjectMode, "subjects": item.Subjects, "status": "pending",
			}}, &create); createErr != nil {
				return summary, createErr
			}
			summary.SetsCreated++
			continue
		}
		if get.FactorSet.SpaceID != item.SpaceID || get.FactorSet.SourceDatasetID != item.SourceDatasetID || get.FactorSet.Freq != item.Freq {
			return summary, fmt.Errorf("factor set %q already exists with a different source identity", setID)
		}
		if get.FactorSet.SubjectMode != item.SubjectMode || !slices.Equal(cleanStrings(get.FactorSet.Subjects), item.Subjects) {
			updated := factorAPIResponse{}
			if updateErr := r.call(ctx, "UpdateFactorSet", map[string]any{
				"set_id": setID, "subject_mode": item.SubjectMode, "subjects": item.Subjects,
			}, &updated); updateErr != nil {
				return summary, updateErr
			}
		}
		summary.Unchanged++
	}
	for _, item := range items {
		get := factorAPIResponse{}
		err := r.call(ctx, "GetFactor", map[string]any{"factor_id": item.FactorID}, &get)
		if err != nil && !factorAPINotFound(get) {
			return summary, err
		}
		created := false
		if err != nil {
			create := factorAPIResponse{}
			if createErr := r.call(ctx, "CreateFactor", map[string]any{"factor": map[string]any{
				"factor_type": item.FactorType, "factor_id": item.FactorID, "set_id": item.SetID,
				"name": item.Name, "source_code": item.SourceCode, "source_hash": item.SourceHash,
				"input_columns": item.InputColumns, "outputs": item.Outputs, "params_json": item.ParamsJSON,
				"lookback_periods": item.LookbackPeriods, "status": "disabled",
			}}, &create); createErr != nil {
				return summary, createErr
			}
			summary.Imported++
			created = true
		} else {
			if !sameFactorContract(get.Factor, item) {
				return summary, fmt.Errorf("factor %q already exists with a different definition; update moox.toml or replace it explicitly", item.FactorID)
			}
			summary.Unchanged++
		}
		currentStatus := "disabled"
		if !created {
			currentStatus = get.Factor.Status
		}
		if currentStatus != item.Status {
			status := factorAPIResponse{}
			if err := r.call(ctx, "SetFactorStatus", map[string]any{"factor_id": item.FactorID, "status": item.Status}, &status); err != nil {
				return summary, err
			}
		}
		summary.Associated++
	}
	return summary, nil
}

func factorAPINotFound(response factorAPIResponse) bool {
	return response.RetInfo.Code == 9 || response.RetInfo.Code == 5 ||
		(response.RetInfo.Code == 4 && strings.Contains(strings.ToLower(response.RetInfo.Msg), "not found"))
}

func sameFactorContract(got struct {
	FactorType      string   `json:"factor_type"`
	FactorID        string   `json:"factor_id"`
	SetID           string   `json:"set_id"`
	Name            string   `json:"name"`
	SourceCode      string   `json:"source_code"`
	SourceHash      string   `json:"source_hash"`
	InputColumns    []string `json:"input_columns"`
	Outputs         []string `json:"outputs"`
	ParamsJSON      string   `json:"params_json"`
	LookbackPeriods int      `json:"lookback_periods"`
	Status          string   `json:"status"`
}, want setupFactorItem) bool {
	if got.FactorType != want.FactorType {
		return false
	}
	if got.SetID != want.SetID || got.SourceHash != want.SourceHash || got.Name != want.Name || got.LookbackPeriods != want.LookbackPeriods || !slicesEqual(got.InputColumns, want.InputColumns) || !slicesEqual(got.Outputs, want.Outputs) {
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

func sortedFactorItems(items []setupFactorItem) []setupFactorItem {
	result := append([]setupFactorItem(nil), items...)
	sort.Slice(result, func(i, j int) bool { return result[i].FactorID < result[j].FactorID })
	return result
}

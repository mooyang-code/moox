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
	"sort"
	"strings"
	"time"

	adminclient "github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

var factorIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type setupFactorItem struct {
	FactorID        string
	Name            string
	SourceCode      string
	SourceHash      string
	InputColumns    []string
	Outputs         []string
	ParamsJSON      string
	LookbackPeriods int
	SpaceID         string
	SourceViewID    string
	Freq            string
	SubjectMode     string
	Subjects        []string
	Status          string
}

type setupFactorSummary struct {
	Enabled   bool `json:"enabled"`
	Planned   int  `json:"planned"`
	Imported  int  `json:"imported"`
	Bound     int  `json:"bound"`
	Unchanged int  `json:"unchanged"`
}

type setupInitFactor interface {
	Apply(context.Context, []setupFactorItem) (setupFactorSummary, error)
	Close() error
}

// These defaults keep older custom.toml files useful after upgrading. Users
// can replace the list in custom.toml when they need another View contract.
func defaultSetupFactorItems() []setupconfig.FactorSetupItem {
	return []setupconfig.FactorSetupItem{
		{
			FactorID: "bias", File: "timeseries/bias.py", Name: "bias",
			InputColumns: []string{"close"}, Outputs: []string{"bias_5", "bias_20"},
			ParamsJSON: `{"windows":[5,20]}`, LookbackPeriods: 20,
			SpaceID: "crypto_market", SourceViewID: "binance_spot_kline_1m_view", Freq: "1m",
			SubjectMode: "all", Status: "enabled",
		},
		{
			FactorID: "cci", File: "timeseries/cci.py", Name: "cci",
			InputColumns: []string{"high", "low", "close"}, Outputs: []string{"cci"},
			ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
			SpaceID: "crypto_market", SourceViewID: "binance_spot_kline_1m_view", Freq: "1m",
			SubjectMode: "all", Status: "enabled",
		},
	}
}

func loadSetupFactors(manifest setupconfig.Manifest, repoRoot string) ([]setupFactorItem, error) {
	if !manifest.Factors.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(manifest.Factors.SourceDir) == "" {
		manifest.Factors.SourceDir = "examples/factors"
	}
	items := manifest.Factors.Items
	if len(items) == 0 {
		defaultRoot := filepath.Join(repoRoot, filepath.FromSlash(manifest.Factors.SourceDir))
		if _, err := os.Stat(defaultRoot); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("stat factors directory: %w", err)
		}
		items = defaultSetupFactorItems()
	}
	root := filepath.Join(repoRoot, filepath.FromSlash(manifest.Factors.SourceDir))
	result := make([]setupFactorItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
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
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = factorID
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
		subjectMode := strings.TrimSpace(item.SubjectMode)
		if subjectMode == "" {
			subjectMode = "all"
		}
		subjects := cleanStrings(item.Subjects)
		if subjectMode == "include" && len(subjects) == 0 {
			return nil, fmt.Errorf("factor %q include binding requires subjects", factorID)
		}
		hash := sha256.Sum256([]byte(sourceCode))
		result = append(result, setupFactorItem{
			FactorID: factorID, Name: name, SourceCode: sourceCode, SourceHash: hex.EncodeToString(hash[:]),
			InputColumns: inputColumns, Outputs: outputs, ParamsJSON: params, LookbackPeriods: item.LookbackPeriods,
			SpaceID: strings.TrimSpace(item.SpaceID), SourceViewID: strings.TrimSpace(item.SourceViewID), Freq: strings.TrimSpace(item.Freq),
			SubjectMode: subjectMode, Subjects: subjects, Status: status,
		})
	}
	return result, nil
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
	secretRaw, err := readRemoteControlFile(ctx, transport, "moox/prod/secrets/gateway-moox-cli.key")
	if err != nil {
		_ = listener.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("factor_gateway_credentials_unavailable")
	}
	envRaw, err := readRemoteControlFile(ctx, transport, "moox/prod/secrets/gateway-service.env")
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
		FactorID        string   `json:"factor_id"`
		Name            string   `json:"name"`
		SourceCode      string   `json:"source_code"`
		SourceHash      string   `json:"source_hash"`
		InputColumns    []string `json:"input_columns"`
		Outputs         []string `json:"outputs"`
		ParamsJSON      string   `json:"params_json"`
		LookbackPeriods int      `json:"lookback_periods"`
		Status          string   `json:"status"`
	} `json:"factor"`
	Binding    factorAPIBinding     `json:"binding"`
	Bindings   []factorAPIBinding   `json:"bindings"`
	PageResult *factorAPIPageResult `json:"page_result"`
}

type factorAPIPageResult struct {
	HasMore bool `json:"has_more"`
}

type factorAPIBinding struct {
	BindingID string `json:"binding_id"`
	FactorID  string `json:"factor_id"`
	Status    string `json:"status"`
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
	for _, item := range items {
		get := factorAPIResponse{}
		err := r.call(ctx, "GetFactor", map[string]any{"factor_id": item.FactorID}, &get)
		notFound := get.RetInfo.Code == 9 || get.RetInfo.Code == 5 ||
			(get.RetInfo.Code == 4 && strings.Contains(strings.ToLower(get.RetInfo.Msg), "not found"))
		if err != nil && !notFound {
			return summary, err
		}
		if err != nil {
			create := factorAPIResponse{}
			if createErr := r.call(ctx, "CreateFactor", map[string]any{"factor": map[string]any{
				"factor_id": item.FactorID, "name": item.Name, "source_code": item.SourceCode,
				"input_columns": item.InputColumns, "outputs": item.Outputs, "params_json": item.ParamsJSON,
				"lookback_periods": item.LookbackPeriods, "status": "disabled",
			}}, &create); createErr != nil {
				return summary, createErr
			}
			summary.Imported++
		} else {
			if !sameFactorContract(get.Factor, item) {
				return summary, fmt.Errorf("factor %q already exists with a different definition; update custom.toml or replace it explicitly", item.FactorID)
			}
			summary.Unchanged++
		}

		subjectsJSON, _ := json.Marshal(item.Subjects)
		bindingID := "setup-" + item.FactorID + "-" + item.SpaceID + "-" + item.SourceViewID + "-" + item.Freq
		// Keep a newly-created (or previously disabled) factor non-executable
		// until the Result View contract has been reconciled.  The Factor RPC
		// treats disabled factors as immediately ready, so sending an enabled
		// binding before SetFactorStatus would expose a small cross-RPC window
		// in which a queued source-ready event can run against an unbuilt View.
		initialStatus := item.Status
		if item.Status == "enabled" && (get.Factor.Status != "enabled") {
			initialStatus = "disabled"
		}
		binding := map[string]any{
			"binding_id": bindingID, "factor_id": item.FactorID, "space_id": item.SpaceID,
			"source_view_id": item.SourceViewID, "freq": item.Freq, "subject_mode": item.SubjectMode,
			"subjects_json": string(subjectsJSON), "status": initialStatus,
		}
		upsert := factorAPIResponse{}
		if err := r.callWithSourceViewRetry(ctx, "UpsertBinding", map[string]any{"binding": binding}, &upsert); err != nil {
			return summary, err
		}
		if initialStatus == "enabled" && upsert.Binding.Status != "" && upsert.Binding.Status != "enabled" {
			return summary, fmt.Errorf("factor %q binding is %s", item.FactorID, upsert.Binding.Status)
		}
		if item.Status == "enabled" {
			status := factorAPIResponse{}
			if err := r.call(ctx, "SetFactorStatus", map[string]any{"factor_id": item.FactorID, "status": "enabled"}, &status); err != nil {
				return summary, err
			}
			// CreateFactor starts disabled. The first UpsertBinding therefore
			// cannot observe an in-progress Result View build. Reconcile once
			// more after enabling the Factor so a fresh install remains pending
			// until the Result View is actually readable.
			binding["status"] = "enabled"
			ready := factorAPIResponse{}
			if err := r.callWithSourceViewRetry(ctx, "UpsertBinding", map[string]any{"binding": binding}, &ready); err != nil {
				return summary, err
			}
			if ready.Binding.Status != "" && ready.Binding.Status != "enabled" {
				return summary, fmt.Errorf("factor %q binding is %s", item.FactorID, ready.Binding.Status)
			}
		} else {
			status := factorAPIResponse{}
			if err := r.call(ctx, "SetFactorStatus", map[string]any{"factor_id": item.FactorID, "status": "disabled"}, &status); err != nil {
				return summary, err
			}
		}
		summary.Bound++
	}
	if err := r.removeObsoleteSetupBindings(ctx, items); err != nil {
		return summary, err
	}
	return summary, nil
}

func (r *remoteSetupFactor) removeObsoleteSetupBindings(ctx context.Context, items []setupFactorItem) error {
	desired := make(map[string]struct{}, len(items))
	for _, item := range items {
		desired["setup-"+item.FactorID+"-"+item.SpaceID+"-"+item.SourceViewID+"-"+item.Freq] = struct{}{}
	}
	var obsolete []string
	for page := 1; ; page++ {
		list := factorAPIResponse{}
		if err := r.call(ctx, "ListBindings", map[string]any{"page": map[string]any{"page": page, "size": 1000}}, &list); err != nil {
			return err
		}
		for _, binding := range list.Bindings {
			if !strings.HasPrefix(binding.BindingID, "setup-") {
				continue
			}
			if _, ok := desired[binding.BindingID]; ok {
				continue
			}
			obsolete = append(obsolete, binding.BindingID)
		}
		if list.PageResult == nil || !list.PageResult.HasMore {
			break
		}
		if page >= 10000 {
			return fmt.Errorf("ListBindings pagination exceeded 10000 pages")
		}
	}
	// Collect all IDs before deleting. The server uses offset pagination, so
	// deleting page 1 while traversing would shift page 2 and skip records.
	for _, bindingID := range obsolete {
		removed := factorAPIResponse{}
		if err := r.call(ctx, "DeleteBinding", map[string]any{"binding_id": bindingID}, &removed); err != nil {
			return err
		}
	}
	return nil
}

func sameFactorContract(got struct {
	FactorID        string   `json:"factor_id"`
	Name            string   `json:"name"`
	SourceCode      string   `json:"source_code"`
	SourceHash      string   `json:"source_hash"`
	InputColumns    []string `json:"input_columns"`
	Outputs         []string `json:"outputs"`
	ParamsJSON      string   `json:"params_json"`
	LookbackPeriods int      `json:"lookback_periods"`
	Status          string   `json:"status"`
}, want setupFactorItem) bool {
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

func (r *remoteSetupFactor) callWithSourceViewRetry(ctx context.Context, method string, body any, response *factorAPIResponse) error {
	const retryInterval = time.Second
	const maxWait = 35 * time.Second
	deadline := time.Now().Add(maxWait)
	for {
		*response = factorAPIResponse{}
		err := r.call(ctx, method, body, response)
		pendingView := method == "UpsertBinding" && response.Binding.Status == "pending_view"
		if err == nil && !pendingView {
			return nil
		}
		if err != nil && !strings.Contains(err.Error(), "must have an active index") {
			return err
		}
		if time.Now().After(deadline) {
			if pendingView {
				return fmt.Errorf("factor binding is still pending_view after %s", maxWait)
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryInterval):
		}
	}
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

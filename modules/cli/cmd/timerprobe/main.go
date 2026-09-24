package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "access-read" {
		if err := accessRead(); err != nil {
			panic(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "view-read" {
		if err := viewRead(); err != nil {
			panic(err)
		}
		return
	}
	client := newClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{BizType: "market_fetcher"})
	if err != nil {
		panic(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "inspect" {
		inspect(nodes)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "meta" {
		dumpMeta(nodes)
		return
	}
	invoke(ctx, client, nodes)
}

func newClient() *adminclient.Client {
	base := strings.TrimRight(os.Getenv("MOOX_CONTROL_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:60675"
	}
	client := adminclient.New(base)
	client.SpaceID = "crypto"
	client.AccessToken = strings.TrimSpace(os.Getenv("MOOX_ACCESS_TOKEN"))
	client.HTTPClient = &http.Client{
		Timeout:   2 * time.Minute,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	if os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID") != "" {
		client.ServiceAuth = &adminclient.ServiceAuthConfig{
			AccessKey:  os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"),
			SecretKey:  os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"),
			Caller:     firstNonEmpty(os.Getenv("MOOX_GATEWAY_CALLER"), "moox-cli"),
			TargetNode: firstNonEmpty(os.Getenv("MOOX_GATEWAY_TARGET_NODE"), "control"),
			CAFile:     os.Getenv("MOOX_GATEWAY_CA_FILE"),
			ExpireSecs: 60,
		}
	}
	return client
}

func inspect(nodes []adminclient.CloudNode) {
	type row struct {
		Name, NS, Dataset, Freq, Market, Cron, Pkg string
		Enabled, Actual                            bool
		Subjects                                   int
	}
	rows := make([]row, 0)
	for _, node := range nodes {
		if !strings.Contains(node.FunctionName, "crypto-binance") {
			continue
		}
		env := nestedMap(node.Metadata, "managed_environment")
		if env == nil {
			env = nestedMap(node.Metadata, "environment")
		}
		subjects := strings.TrimSpace(metaString(env, "MOOX_MARKET_FETCH_SUBJECTS"))
		count := 0
		if subjects != "" {
			count = len(strings.Split(subjects, "|"))
		}
		rows = append(rows, row{
			Name:     node.FunctionName,
			NS:       node.Namespace,
			Dataset:  metaString(env, "MOOX_MARKET_FETCH_DATASET_ID"),
			Freq:     metaString(env, "MOOX_MARKET_FETCH_FREQUENCY"),
			Market:   metaString(env, "MOOX_MARKET_FETCH_MARKET_TYPE"),
			Cron:     firstNonEmpty(metaString(node.Metadata, "timer_actual_cron"), metaString(node.Metadata, "timer_cron")),
			Pkg:      node.PackageID,
			Enabled:  metaBool(node.Metadata, "timer_enabled"),
			Actual:   metaBool(node.Metadata, "timer_actual_enabled"),
			Subjects: count,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rows)
	fmt.Fprintf(os.Stderr, "crypto functions: %d\n", len(rows))
}

func dumpMeta(nodes []adminclient.CloudNode) {
	want := strings.TrimSpace(os.Getenv("MOOX_PROBE_META_FUNCTION"))
	if want == "" {
		want = "moox-fetcher-crypto-binance-ap-nanjing-50"
	}
	for _, node := range nodes {
		if node.FunctionName != want {
			continue
		}
		keys := make([]string, 0, len(node.Metadata))
		for key := range node.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Println("keys", keys)
		raw, _ := json.MarshalIndent(node.Metadata, "", "  ")
		if len(raw) > 4000 {
			raw = raw[:4000]
		}
		fmt.Println(string(raw))
	}
}

func invoke(ctx context.Context, client *adminclient.Client, nodes []adminclient.CloudNode) {
	want := map[string]bool{}
	if raw := strings.TrimSpace(os.Getenv("MOOX_PROBE_FUNCTIONS")); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			want[strings.TrimSpace(name)] = true
		}
	} else {
		want["moox-fetcher-crypto-binance-invoke-ap-hongkong-0"] = true
	}
	event := map[string]any{
		"action":                     "market_fetch",
		"request_id":                 "manual-kline-probe",
		"storage_rpc_gateway_target": "ip://146.56.196.204:11003",
		"data": map[string]any{
			"batch_id":        "manual-kline-probe",
			"schedule_id":     "manual-kline-probe-schedule",
			"batch_kind":      "realtime",
			"space_id":        "crypto",
			"market_id":       "crypto",
			"instrument_type": "spot",
			"dataset_id":      "dataset_binance_spot_kline_1m",
			"frequency":       "1m",
			"provider":        "binance",
			"source_id":       "spot_http",
			"market_type":     "spot",
			"region":          "ap-hongkong",
			"node_id":         "moox-fetcher-crypto-binance-invoke-ap-hongkong-0",
			"items": []map[string]any{{
				"subject_id": "BTC-USDT", "symbol": "BTCUSDT", "provider": "binance", "source_id": "spot_http",
				"market_type": "spot", "data_type": "kline", "dataset_id": "dataset_binance_spot_kline_1m", "frequency": "1m", "bar_limit": 2,
			}},
		},
	}
	for _, node := range nodes {
		if !want[node.FunctionName] {
			continue
		}
		fmt.Printf("==== %s node=%s ns=%s pkg=%v\n", node.FunctionName, node.NodeID, node.Namespace, node.PackageID)
		resp, invErr := client.InvokeFunction(ctx, node.NodeID, event)
		if invErr != nil {
			fmt.Printf("invoke error: %v\n", invErr)
			continue
		}
		data, _ := resp["data"].(map[string]any)
		if data == nil {
			raw, _ := json.Marshal(resp)
			fmt.Printf("resp %s\n", raw)
			continue
		}
		fmt.Printf("dataset=%v freq=%v status=%v err=%v duration=%v\n", data["dataset_id"], data["frequency"], data["status"], data["error_summary"], data["duration_ms"])
		if items, ok := data["items"].([]any); ok && len(items) > 0 {
			raw, _ := json.Marshal(items[0])
			fmt.Printf("item0 %s\n", raw)
			if len(items) > 1 {
				raw, _ = json.Marshal(items[1])
				fmt.Printf("item1 %s\n", raw)
			}
		}
	}
}

func nestedMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if child, ok := m[key].(map[string]any); ok {
		return child
	}
	return nil
}

func metaString(m map[string]any, key string) string {
	if m == nil || m[key] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(m[key]))
}

func metaBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true") || v == "1"
	default:
		s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		return s == "true" || s == "1"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

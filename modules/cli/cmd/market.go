package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var marketFlags struct {
	ControlURL, MarketID, InstrumentTypes, Subjects, Frequency, Start, End, Order, Cursor string
	PageSize                                                                              int32
}

var marketCmd = &cobra.Command{Use: "market", Short: "Query logical built-in markets"}
var marketStatusCmd = &cobra.Command{Use: "status", RunE: func(cmd *cobra.Command, _ []string) error {
	rsp := &pb.GetMarketStatusRsp{}
	if err := postCollectorMarket(cmd.Context(), marketFlags.ControlURL, "GetMarketStatus", &pb.GetMarketStatusReq{MarketId: marketFlags.MarketID}, rsp); err != nil {
		return err
	}
	return writeProtoJSON(cmd, rsp)
}}
var marketKlineCmd = &cobra.Command{Use: "kline"}
var marketKlineQueryCmd = &cobra.Command{Use: "query", RunE: func(cmd *cobra.Command, _ []string) error {
	rsp := &pb.QueryMarketKlinesRsp{}
	req := &pb.QueryMarketKlinesReq{MarketId: marketFlags.MarketID, InstrumentTypes: splitCSV(marketFlags.InstrumentTypes), SubjectIds: splitCSV(marketFlags.Subjects), Frequency: marketFlags.Frequency, StartTime: marketFlags.Start, EndTime: marketFlags.End, Order: marketFlags.Order, PageSize: marketFlags.PageSize, Cursor: marketFlags.Cursor}
	if err := postCollectorMarket(cmd.Context(), marketFlags.ControlURL, "QueryMarketKlines", req, rsp); err != nil {
		return err
	}
	return writeProtoJSON(cmd, rsp)
}}
var marketKlineRefreshCmd = &cobra.Command{Use: "refresh", RunE: func(cmd *cobra.Command, _ []string) error {
	rsp := &pb.RefreshMarketKlinesRsp{}
	req := &pb.RefreshMarketKlinesReq{MarketId: marketFlags.MarketID, InstrumentTypes: splitCSV(marketFlags.InstrumentTypes), SubjectIds: splitCSV(marketFlags.Subjects), Frequency: marketFlags.Frequency, StartTime: marketFlags.Start, EndTime: marketFlags.End}
	if err := postCollectorMarket(cmd.Context(), marketFlags.ControlURL, "RefreshMarketKlines", req, rsp); err != nil {
		return err
	}
	return writeProtoJSON(cmd, rsp)
}}

func postCollectorMarket(ctx context.Context, baseURL, method string, req, rsp proto.Message) error {
	if strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("--control-url is required")
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(req)
	if err != nil {
		return err
	}
	url := strings.TrimRight(baseURL, "/") + "/trpc.moox.collector.CollectMgr/" + method
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpRsp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return err
	}
	defer httpRsp.Body.Close()
	body, _ := io.ReadAll(httpRsp.Body)
	if httpRsp.StatusCode != http.StatusOK {
		return fmt.Errorf("collector %s HTTP %d: %s", method, httpRsp.StatusCode, body)
	}
	return protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(body, rsp)
}

func writeProtoJSON(cmd *cobra.Command, value proto.Message) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true, Multiline: true, Indent: "  "}.Marshal(value)
	if err != nil {
		return err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(normalized)
}

func splitCSV(raw string) []string {
	result := []string{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func init() {
	rootCmd.AddCommand(marketCmd)
	marketCmd.AddCommand(marketStatusCmd, marketKlineCmd)
	marketKlineCmd.AddCommand(marketKlineQueryCmd, marketKlineRefreshCmd)
	for _, command := range []*cobra.Command{marketStatusCmd, marketKlineQueryCmd, marketKlineRefreshCmd} {
		command.Flags().StringVar(&marketFlags.ControlURL, "control-url", "", "Collector control URL")
		command.Flags().StringVar(&marketFlags.MarketID, "market", "", "logical market ID")
		_ = command.MarkFlagRequired("market")
	}
	for _, command := range []*cobra.Command{marketKlineQueryCmd, marketKlineRefreshCmd} {
		command.Flags().StringVar(&marketFlags.InstrumentTypes, "instrument-types", "", "comma-separated instrument types")
		command.Flags().StringVar(&marketFlags.Subjects, "subjects", "", "comma-separated subject IDs")
		command.Flags().StringVar(&marketFlags.Frequency, "frequency", "", "K-line frequency")
		command.Flags().StringVar(&marketFlags.Start, "start", "", "RFC3339 range start")
		command.Flags().StringVar(&marketFlags.End, "end", "", "RFC3339 range end")
		_ = command.MarkFlagRequired("subjects")
		_ = command.MarkFlagRequired("frequency")
		_ = command.MarkFlagRequired("start")
		_ = command.MarkFlagRequired("end")
	}
	marketKlineQueryCmd.Flags().StringVar(&marketFlags.Order, "order", "asc", "asc or desc")
	marketKlineQueryCmd.Flags().StringVar(&marketFlags.Cursor, "cursor", "", "opaque query cursor")
	marketKlineQueryCmd.Flags().Int32Var(&marketFlags.PageSize, "page-size", 100, "page size")
}

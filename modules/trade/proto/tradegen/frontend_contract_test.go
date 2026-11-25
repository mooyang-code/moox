package tradepb

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// 管理台“手工登记下架”的预览（web/src/api/trade/delist.ts）按字段约定读
// ListInstrumentHolders：请求用小写的交易所与市场，响应有 holders[] 各字段和 instrument_unknown。proto 一改，这条测试就会提醒前端同步。
func readFrontendFile(t *testing.T, relative string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", relative))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// tsInterfaceFields 取 TypeScript 接口里声明的字段名。
func tsInterfaceFields(t *testing.T, source, name string) []string {
	t.Helper()
	block := regexp.MustCompile(`(?s)export interface ` + name + ` \{(.*?)\n\}`).FindStringSubmatch(source)
	if block == nil {
		t.Fatalf("前端没有声明接口 %s", name)
	}
	var fields []string
	for _, match := range regexp.MustCompile(`(?m)^\s+(\w+)\??:`).FindAllStringSubmatch(block[1], -1) {
		fields = append(fields, match[1])
	}
	sort.Strings(fields)
	return fields
}

func protoFieldNames(message protoreflect.MessageDescriptor) []string {
	var names []string
	for i := 0; i < message.Fields().Len(); i++ {
		names = append(names, string(message.Fields().Get(i).Name()))
	}
	sort.Strings(names)
	return names
}

func TestFrontendDelistPreviewMatchesTheHoldersContract(t *testing.T) {
	source := readFrontendFile(t, "web/src/api/trade/delist.ts")

	// 请求：字段名和 proto 一致（前端把可读的交易所、市场换成枚举名）。
	request := (&ListInstrumentHoldersReq{}).ProtoReflect().Descriptor()
	if got, want := tsInterfaceFields(t, source, "ListInstrumentHoldersReq"), protoFieldNames(request); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("前端请求字段 %v 与 proto %v 不一致", got, want)
	}
	// 响应里的每一项：字段名和 proto 的 InstrumentHolder 完全一致。
	holder := (&InstrumentHolder{}).ProtoReflect().Descriptor()
	if got, want := tsInterfaceFields(t, source, "InstrumentHolder"), protoFieldNames(holder); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("前端持有者字段 %v 与 proto %v 不一致", got, want)
	}
	// 响应：前端读 holders 和 instrument_unknown。
	response := (&ListInstrumentHoldersRsp{}).ProtoReflect().Descriptor()
	for _, field := range []string{"holders", "instrument_unknown"} {
		if response.Fields().ByName(protoreflect.Name(field)) == nil {
			t.Errorf("响应没有字段 %s", field)
		}
		if !strings.Contains(source, field) {
			t.Errorf("前端没有读响应字段 %s", field)
		}
	}
	// 请求里的交易所与市场直接用前端类型的取值，必须在 Trade 的小写白名单里。
	for typeName, valid := range map[string]func(string) bool{"DelistExchange": validExchange, "DelistMarket": validDelistMarket} {
		declaration := regexp.MustCompile(`export type ` + typeName + ` = ([^;]+);`).FindStringSubmatch(source)
		if declaration == nil {
			t.Fatalf("前端没有类型 %s", typeName)
		}
		used := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(declaration[1], -1)
		if len(used) == 0 {
			t.Fatalf("%s 里没有取值", typeName)
		}
		for _, match := range used {
			if !valid(match[1]) {
				t.Errorf("前端 %s 的取值 %q 不被 Trade 接受", typeName, match[1])
			}
		}
	}
}

// 响应经网关的 JSON 序列化后：每个持有者都带全部字段（含 false 的 dust、paper），模拟盘的项 paper 为 true；
// 没有持有者时有 holders: []。
func TestHoldersResponseJSONCarriesEveryFieldTheFrontendReads(t *testing.T) {
	raw := marshalWithTRPCJSON(t, &ListInstrumentHoldersRsp{
		RetInfo: &RetInfo{},
		Holders: []*InstrumentHolder{{
			SpaceId: "crypto", LogicalAccountId: "la-1", TradingAccountId: "ta-1", Quantity: "12.50", Dust: false, Paper: true,
		}},
	})
	for _, want := range []string{
		`"space_id":"crypto"`, `"logical_account_id":"la-1"`, `"trading_account_id":"ta-1"`,
		`"quantity":"12.50"`, `"dust":false`, `"paper":true`, `"instrument_unknown":false`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("响应 JSON 缺 %s：%s", want, raw)
		}
	}
}

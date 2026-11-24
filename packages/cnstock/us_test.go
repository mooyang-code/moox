package cnstock

import (
	"strings"
	"testing"
)

func usLine(symbol, status string) string {
	fields := make([]string, 73)
	fields[0], fields[2], fields[40] = "200", strings.TrimPrefix(symbol, "us")+".OQ", status
	return "v_" + symbol + `="` + strings.Join(fields, "~") + `";`
}

func TestUSTencentSymbol(t *testing.T) {
	for in, want := range map[string]string{"AAPL.XNAS": "usAAPL", "BRK.B.XNYS": "usBRK.B", "spy.XASE": "usSPY"} {
		got, err := USTencentSymbol(in)
		if err != nil || got != want {
			t.Fatalf("%s → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "AAPL", ".XNAS", "AAPL."} {
		if _, err := USTencentSymbol(bad); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
}

// 第 40 个字段：S 停牌、D 退市、空正常；不在对照里的行和格式不对的行跳过。
func TestParseTencentUSStatus(t *testing.T) {
	body := usLine("usAAPL", "") + "\n" + usLine("usIPFX", "S") + "\n" + usLine("usGONE", "D") + "\n" +
		usLine("usOTHER", "S") + "\n" + `v_usSHORT="200~x";` + "\n" + `v_pv_none_match="1";`
	got := ParseTencentUSStatus([]byte(body), map[string]string{"usaapl": "AAPL.XNAS", "usipfx": "IPFX.XNAS", "usgone": "GONE.XNYS", "usshort": "SHORT.XNAS"})
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	if q := got["AAPL.XNAS"]; q.Halted || q.Delisted {
		t.Fatalf("AAPL 正常：%+v", q)
	}
	if q := got["IPFX.XNAS"]; !q.Halted || q.Delisted {
		t.Fatalf("IPFX 停牌：%+v", q)
	}
	if q := got["GONE.XNYS"]; q.Halted || !q.Delisted {
		t.Fatalf("GONE 退市：%+v", q)
	}
}

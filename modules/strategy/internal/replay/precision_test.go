package replay

import (
	"math"
	"testing"
)

// 有限输入不得产生非有限输出：很大的值放大会溢出，但此时浮点分辨率已经粗于所需精度，原样返回。
func TestRoundPlacesKeepsFiniteValuesFinite(t *testing.T) {
	for _, value := range []float64{1e301, -1e301, 1e300, math.MaxFloat64, 1e20, 123456789012.34567} {
		if got := roundPlaces(value, quantityPlaces); math.IsInf(got, 0) || math.IsNaN(got) {
			t.Errorf("%g 取整后变成了 %g", value, got)
		}
	}
	if got := round4(0.123456); got != 0.1235 {
		t.Errorf("正常取整：%v", got)
	}
	if got := round4(-0.00001); got != 0 || math.Signbit(got) {
		t.Errorf("-0 应规范为 0：%v", got)
	}
}

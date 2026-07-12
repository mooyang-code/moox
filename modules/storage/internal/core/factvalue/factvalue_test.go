package factvalue

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestString_Nil_ShouldReturnEmpty(t *testing.T) {
	assert.Equal(t, "", String(nil))
}

func TestString_AllTypedVariants_ShouldFormat(t *testing.T) {
	cases := []struct {
		name string
		in   *pb.TypedValue
		want string
	}{
		{"string", &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "abc"}}, "abc"},
		{"int", &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 42}}, "42"},
		{"double", &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}, "1.5"},
		{"bool", &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: true}}, "true"},
		{"time", &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: "2026-07-08T00:00:00Z"}}, "2026-07-08T00:00:00Z"},
		{"json", &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: `{"k":1}`}}, `{"k":1}`},
		{"bytes", &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: []byte("hi")}}, "hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, String(tc.in))
		})
	}
}

func TestNumeric_SupportedAndUnsupported(t *testing.T) {
	n, ok := Numeric(&pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 7}})
	assert.True(t, ok)
	assert.Equal(t, 7.0, n)

	n, ok = Numeric(&pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2.5}})
	assert.True(t, ok)
	assert.Equal(t, 2.5, n)

	_, ok = Numeric(&pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}})
	assert.False(t, ok)
}

func TestCompare_NumericAndText(t *testing.T) {
	left := &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 1}}
	right := &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}
	assert.Equal(t, -1, Compare(left, right))
	assert.Equal(t, 1, Compare(right, left))
	assert.Equal(t, 0, Compare(left, left))

	a := &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "a"}}
	b := &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "b"}}
	assert.Equal(t, -1, Compare(a, b))
}

func TestStringSet_EmptyAndValues(t *testing.T) {
	assert.Nil(t, StringSet(nil))
	assert.Nil(t, StringSet([]string{}))
	got := StringSet([]string{"a", "b"})
	assert.True(t, got["a"])
	assert.True(t, got["b"])
	assert.False(t, got["c"])
}

func TestParseTime_ValidAndInvalid(t *testing.T) {
	ts, ok := ParseTime("2026-07-08T06:12:00.000000000Z")
	require.True(t, ok)
	assert.False(t, ts.IsZero())

	_, ok = ParseTime("")
	assert.False(t, ok)
	_, ok = ParseTime("bad")
	assert.False(t, ok)
}

func TestTimeInRange_NilRange_ShouldAlwaysTrue(t *testing.T) {
	assert.True(t, TimeInRange("2026-07-08T06:12:00Z", nil))
}

func TestTimeInRange_WithinAndOutside(t *testing.T) {
	tr := &pb.TimeRange{
		StartTime: "2026-07-08T06:00:00Z",
		EndTime:   "2026-07-08T07:00:00Z",
	}
	assert.True(t, TimeInRange("2026-07-08T06:30:00Z", tr))
	assert.False(t, TimeInRange("2026-07-08T05:59:59Z", tr))
	assert.False(t, TimeInRange("2026-07-08T07:00:01Z", tr))
}

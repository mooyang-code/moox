package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactorConvertRoundTrip(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	def := domain.FactorDef{
		FactorID: "f1", Name: "alpha", Kind: "python", SourceCode: "x=1",
		ParamsJSON: "{}", LookbackBars: 10, WritebackBars: 2, Status: "active",
		CreateTime: now, ModifyTime: now,
	}
	pb := factorToPB(def)
	require.NotNil(t, pb)
	assert.Equal(t, "f1", pb.GetFactorId())
	got := factorFromPB(pb)
	assert.Equal(t, def.FactorID, got.FactorID)
	assert.Equal(t, def.LookbackBars, got.LookbackBars)
	assert.Equal(t, domain.FactorDef{}, factorFromPB(nil))
}

func TestBindingConvertRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	binding := domain.FactorBinding{
		BindingID: "b1", FactorID: "f1", SpaceID: "crypto", SourceDataset: "src",
		TargetDataset: "dst", Freq: "1m", SubjectMode: "single", SubjectsJSON: "[]",
		Status: "active", CreateTime: now, ModifyTime: now,
	}
	pb := bindingToPB(binding)
	got := bindingFromPB(pb)
	assert.Equal(t, binding.BindingID, got.BindingID)
	assert.Equal(t, domain.FactorBinding{}, bindingFromPB(nil))
}

func TestFormatTime(t *testing.T) {
	assert.Equal(t, "", formatTime(time.Time{}))
	ts := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, "2026-02-01T00:00:00Z", formatTime(ts))
}

func TestPageParamsAndResult(t *testing.T) {
	page, size := pageParams(&commonpb.Page{})
	assert.Equal(t, uint32(1), page)
	assert.Equal(t, uint32(50), size)
	result := pageResult(1, 50, 120)
	assert.True(t, result.GetHasMore())
	assert.Equal(t, uint32(120), result.GetTotal())
}

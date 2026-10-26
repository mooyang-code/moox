package domain_test

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/stretchr/testify/require"
)

// 虚拟输入列 is_filled 不要求出现在源数据集里，名称与窗口的虚拟列一致。
func TestMembershipAcceptsVirtualFilledColumn(t *testing.T) {
	require.Equal(t, serieswindow.FilledColumn, domain.FilledColumn)
	set := domain.FactorTask{TaskID: "s", SpaceID: "crypto", SourceDatasetID: "d", Freq: "1m"}
	def := domain.MustDefinition("real_ret", "real_ret", "output: close * (1 - is_filled)\n")
	require.Contains(t, def.InputColumns, domain.FilledColumn)
	columns := []domain.SourceColumn{{Name: "close", Numeric: true}}
	require.NoError(t, domain.ValidateMembership(set, domain.MemberDef{Key: def.FactorID, Factor: def, Kind: domain.MemberKindFixed}, columns, nil, nil))

	bad := domain.MustDefinition("bad", "bad", "output: volume * 2\n")
	require.ErrorContains(t, domain.ValidateMembership(set, domain.MemberDef{Key: bad.FactorID, Factor: bad, Kind: domain.MemberKindFixed}, columns, nil, nil), "unknown input column")
}

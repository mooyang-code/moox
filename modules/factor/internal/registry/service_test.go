package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestImportFactorFileUsesPeriodsAndDepends(t *testing.T) {
	dir := t.TempDir()
	source := "extra_data_dict = {'x': ['funding_rate']}\ndef signal(*args): return args[0]\n"
	path := filepath.Join(dir, "Bias.py")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o644))
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(factorschema.AllSQL()).Error)
	svc := NewService(store.NewFactorRepository(db), nil, Options{
		FactorsDir: dir, DefaultPeriods: []int{20},
	})
	factor, err := svc.ImportFactorFile(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, []int{20}, factor.Periods)
	require.Equal(t, []string{"funding_rate"}, factor.Depends)
	require.GreaterOrEqual(t, factor.LookbackBars, 20)
	require.NotEmpty(t, factor.SourcePath)
}

func TestDefaultLookback(t *testing.T) {
	require.Equal(t, 200, DefaultLookback(nil))
	require.Equal(t, 864, DefaultLookback([]int{20, 96, 288}))
}

func TestResultDataset(t *testing.T) {
	require.Equal(t, "binance_spot_factor", ResultDataset("binance_spot_kline"))
}

package store

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestFactorRepositoryCreateDoesNotOverwriteDuplicateIDOrName(t *testing.T) {
	repo := NewFactorRepository(openTestDB(t))
	original := testFactor("factor-1", domain.FactorStatusEnabled)
	require.NoError(t, repo.Create(context.Background(), original))

	duplicateID := original
	duplicateID.Outputs = []string{"changed"}
	require.Error(t, repo.Create(context.Background(), duplicateID))

	duplicateName := testFactor("factor-2", domain.FactorStatusEnabled)
	duplicateName.Name = original.Name
	require.Error(t, repo.Create(context.Background(), duplicateName))

	got, err := repo.Get(context.Background(), original.FactorID)
	require.NoError(t, err)
	require.Equal(t, original.Outputs, got.Outputs)
}

func TestFactorRepositoryUpdateNeverChangesNameOutputsOrStatus(t *testing.T) {
	repo := NewFactorRepository(openTestDB(t))
	factor := testFactor("factor-1", domain.FactorStatusEnabled)
	require.NoError(t, repo.Create(context.Background(), factor))

	factor.Name = "Renamed"
	factor.Outputs = []string{"changed"}
	factor.ParamsJSON = `{"windows":[10]}`
	factor.Status = domain.FactorStatusDisabled
	require.NoError(t, repo.Update(context.Background(), factor))

	got, err := repo.Get(context.Background(), factor.FactorID)
	require.NoError(t, err)
	require.Equal(t, "Factor_factor-1", got.Name)
	require.Equal(t, []string{"bias_20", "bias_96"}, got.Outputs)
	require.Equal(t, `{"windows":[10]}`, got.ParamsJSON)
	require.Equal(t, domain.FactorStatusEnabled, got.Status)
}

func TestFactorRepositoryDeleteRemovesDefinition(t *testing.T) {
	repo := NewFactorRepository(openTestDB(t))
	factor := testFactor("factor-1", domain.FactorStatusEnabled)
	require.NoError(t, repo.Create(context.Background(), factor))

	require.NoError(t, repo.Delete(context.Background(), factor.FactorID))
	_, err := repo.Get(context.Background(), factor.FactorID)
	require.Error(t, err)
	require.Error(t, repo.Delete(context.Background(), factor.FactorID))
}

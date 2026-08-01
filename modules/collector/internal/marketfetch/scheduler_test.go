package marketfetch

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestRotateRulesAfterAdvancesPastLastCappedRule(t *testing.T) {
	rules := []domain.TaskRule{{RuleID: "rule-a"}, {RuleID: "rule-b"}, {RuleID: "rule-c"}}
	rotated := rotateRulesAfter(rules, "rule-a")

	assert.Equal(t, []string{"rule-b", "rule-c", "rule-a"}, ruleIDs(rotated))
	assert.Equal(t, []string{"rule-a", "rule-b", "rule-c"}, ruleIDs(rotateRulesAfter(rules, "missing")))
}

func TestNormalizedBatchIdentityUsesNormalizedItemMarketType(t *testing.T) {
	provider, marketType := normalizedBatchIdentity(
		domain.CollectionItem{Provider: "binance", MarketType: "spot"},
		domain.TaskRule{Provider: "binance", MarketType: ""},
	)

	assert.Equal(t, "binance", provider)
	assert.Equal(t, "spot", marketType)
}

func ruleIDs(rules []domain.TaskRule) []string {
	ids := make([]string, 0, len(rules))
	for _, rule := range rules {
		ids = append(ids, rule.RuleID)
	}
	return ids
}

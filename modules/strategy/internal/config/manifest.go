package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/packages/report"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "moox.strategy/v2"
	Kind       = "coin_selection"
)

type Manifest struct {
	APIVersion     string             `yaml:"api_version"`
	Kind           string             `yaml:"kind"`
	Input          ManifestInput      `yaml:"input"`
	InstrumentPool InstrumentPoolRule `yaml:"instrument_pool"`
	Schedule       Schedule           `yaml:"schedule"`
	Readiness      Readiness          `yaml:"readiness"`
	Long           *Side              `yaml:"long,omitempty"`
	Short          *Side              `yaml:"short,omitempty"`
}

type ManifestInput struct {
	SourceViewID  string      `yaml:"source_view_id"`
	DataFrequency string      `yaml:"data_frequency"`
	Factors       []FactorRef `yaml:"factors"`
}

type FactorRef struct {
	FactorID string `yaml:"factor_id"`
	// Output selects one named column from a multi-output FactorDef. It may be
	// omitted only when the definition exposes exactly one output.
	Output string `yaml:"output,omitempty"`
}

type InstrumentPoolRule struct {
	Exchanges         []string `yaml:"exchanges"`
	Markets           []string `yaml:"markets"`
	QuoteAssets       []string `yaml:"quote_assets"`
	Include           []string `yaml:"include"`
	Exclude           []string `yaml:"exclude"`
	MinHistoryPeriods int      `yaml:"min_history_periods"`
}

type Schedule struct {
	Every string `yaml:"every"`
}

type Readiness struct {
	Policy string `yaml:"policy"`
}

type ScoreRule struct {
	FactorID  string `yaml:"factor_id"`
	Direction string `yaml:"direction"`
	Weight    string `yaml:"weight"`
}

type FilterRule struct {
	Phase     string `yaml:"phase"`
	FactorID  string `yaml:"factor_id"`
	ValueType string `yaml:"value_type"`
	Op        string `yaml:"op"`
	Value     any    `yaml:"value"`
}

type SelectionRule struct {
	Mode  string `yaml:"mode"`
	Value any    `yaml:"value"`
}

type Side struct {
	SideWeight string        `yaml:"side_weight"`
	Scores     []ScoreRule   `yaml:"scores"`
	Filters    []FilterRule  `yaml:"filters"`
	Selection  SelectionRule `yaml:"selection"`
}

func Parse(raw []byte) (Manifest, error) {
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse strategy manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, errors.New("strategy manifest must contain one YAML document")
		}
		return Manifest{}, fmt.Errorf("parse strategy manifest: %w", err)
	}
	if err := Validate(&manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Validate(manifest *Manifest) error {
	if manifest == nil {
		return errors.New("strategy manifest is required")
	}
	if manifest.APIVersion != APIVersion {
		return fmt.Errorf("strategy manifest api_version must be %s", APIVersion)
	}
	if manifest.Kind != Kind {
		return fmt.Errorf("strategy manifest kind must be %s", Kind)
	}
	if strings.TrimSpace(manifest.Input.SourceViewID) == "" {
		return errors.New("strategy manifest input.source_view_id is required")
	}
	frequency, err := report.NormalizeDatasetFrequency(manifest.Input.DataFrequency)
	if err != nil {
		return fmt.Errorf("strategy manifest input.data_frequency: %w", err)
	}
	manifest.Input.DataFrequency = frequency
	if len(manifest.Input.Factors) == 0 {
		return errors.New("strategy manifest input.factors is required")
	}
	seenFactors := make(map[string]struct{}, len(manifest.Input.Factors))
	for i := range manifest.Input.Factors {
		id := strings.TrimSpace(manifest.Input.Factors[i].FactorID)
		if id == "" {
			return fmt.Errorf("strategy manifest input.factors[%d].factor_id is required", i)
		}
		if _, exists := seenFactors[id]; exists {
			return fmt.Errorf("strategy manifest input.factors contains duplicate %q", id)
		}
		seenFactors[id] = struct{}{}
		manifest.Input.Factors[i].FactorID = id
		manifest.Input.Factors[i].Output = strings.TrimSpace(manifest.Input.Factors[i].Output)
	}
	if manifest.Schedule.Every == "" {
		manifest.Schedule.Every = manifest.Input.DataFrequency
	}
	schedule, err := report.ParseDatasetFrequency(manifest.Schedule.Every)
	if err != nil || schedule <= 0 {
		return fmt.Errorf("strategy manifest schedule.every is invalid")
	}
	dataDuration, err := report.ParseDatasetFrequency(manifest.Input.DataFrequency)
	if err != nil || dataDuration <= 0 || schedule%dataDuration != 0 {
		return errors.New("strategy manifest schedule.every must be a data frequency multiple")
	}
	manifest.Schedule.Every, _ = report.NormalizeDatasetFrequency(manifest.Schedule.Every)
	if manifest.Readiness.Policy == "" {
		manifest.Readiness.Policy = "strict"
	}
	if manifest.Readiness.Policy != "strict" {
		return errors.New("strategy manifest readiness.policy must be strict")
	}
	if err := validatePool(&manifest.InstrumentPool); err != nil {
		return err
	}
	normalizePoolTokens(&manifest.InstrumentPool)
	enabledSide := false
	for sideName, side := range map[string]*Side{"long": manifest.Long, "short": manifest.Short} {
		if side == nil || sideWeight(side.SideWeight).IsZero() {
			continue
		}
		enabledSide = true
		for i, score := range side.Scores {
			if _, ok := seenFactors[score.FactorID]; !ok {
				return fmt.Errorf("strategy manifest %s.scores[%d].factor_id %q is not declared in input.factors", sideName, i, score.FactorID)
			}
		}
		for i, filter := range side.Filters {
			if _, ok := seenFactors[filter.FactorID]; !ok {
				return fmt.Errorf("strategy manifest %s.filters[%d].factor_id %q is not declared in input.factors", sideName, i, filter.FactorID)
			}
		}
	}
	if err := validateSide("long", manifest.Long); err != nil {
		return err
	}
	if err := validateSide("short", manifest.Short); err != nil {
		return err
	}
	if !enabledSide {
		return errors.New("strategy manifest requires a positive long or short side_weight")
	}
	if manifest.Short != nil && sideWeight(manifest.Short.SideWeight).Cmp(quant.Zero()) > 0 && allSpot(manifest.InstrumentPool.Markets) {
		return errors.New("spot-only instrument_pool cannot enable short")
	}
	return nil
}

func normalizePoolTokens(pool *InstrumentPoolRule) {
	pool.Exchanges = normalizeTokens(pool.Exchanges)
	pool.Markets = normalizeTokens(pool.Markets)
	pool.QuoteAssets = normalizeTokens(pool.QuoteAssets)
	pool.Include = normalizeTokens(pool.Include)
	pool.Exclude = normalizeTokens(pool.Exclude)
}

func normalizeTokens(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ToUpper(strings.TrimSpace(value))
	}
	return result
}

func validatePool(pool *InstrumentPoolRule) error {
	if pool.MinHistoryPeriods < 0 {
		return errors.New("strategy manifest instrument_pool.min_history_periods must not be negative")
	}
	for field, values := range map[string][]string{
		"exchanges": pool.Exchanges, "markets": pool.Markets, "quote_assets": pool.QuoteAssets,
		"include": pool.Include, "exclude": pool.Exclude,
	} {
		seen := make(map[string]struct{}, len(values))
		for i, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				return fmt.Errorf("strategy manifest instrument_pool.%s[%d] is empty", field, i)
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("strategy manifest instrument_pool.%s contains duplicate %q", field, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func validateSide(name string, side *Side) error {
	if side == nil {
		return nil
	}
	weight, err := quant.Parse(side.SideWeight)
	if err != nil || weight.IsNegative() {
		return fmt.Errorf("strategy manifest %s.side_weight must be a non-negative decimal", name)
	}
	if weight.IsZero() {
		return nil
	}
	if len(side.Scores) == 0 {
		return fmt.Errorf("strategy manifest %s.scores is required when side is enabled", name)
	}
	for i := range side.Scores {
		if strings.TrimSpace(side.Scores[i].FactorID) == "" {
			return fmt.Errorf("strategy manifest %s.scores[%d].factor_id is required", name, i)
		}
		if side.Scores[i].Direction != "ascending" && side.Scores[i].Direction != "descending" {
			return fmt.Errorf("strategy manifest %s.scores[%d].direction is invalid", name, i)
		}
		value, parseErr := quant.Parse(side.Scores[i].Weight)
		if parseErr != nil || value.IsNegative() || value.IsZero() {
			return fmt.Errorf("strategy manifest %s.scores[%d].weight must be positive", name, i)
		}
	}
	for i := range side.Filters {
		filter := &side.Filters[i]
		if filter.Phase != "pre" && filter.Phase != "post" {
			return fmt.Errorf("strategy manifest %s.filters[%d].phase is invalid", name, i)
		}
		if strings.TrimSpace(filter.FactorID) == "" {
			return fmt.Errorf("strategy manifest %s.filters[%d].factor_id is required", name, i)
		}
		if filter.ValueType != "value" && filter.ValueType != "percentile" {
			return fmt.Errorf("strategy manifest %s.filters[%d].value_type is invalid", name, i)
		}
		switch filter.Op {
		case "lt", "lte", "gt", "gte", "eq":
		default:
			return fmt.Errorf("strategy manifest %s.filters[%d].op is invalid", name, i)
		}
		if _, parseErr := parseDecimalValue(filter.Value); parseErr != nil {
			return fmt.Errorf("strategy manifest %s.filters[%d].value: %w", name, i, parseErr)
		}
	}
	if side.Selection.Mode != "count" && side.Selection.Mode != "fraction" {
		return fmt.Errorf("strategy manifest %s.selection.mode must be count or fraction", name)
	}
	value, err := parseDecimalValue(side.Selection.Value)
	if err != nil || value.IsZero() {
		return fmt.Errorf("strategy manifest %s.selection.value must be positive", name)
	}
	if side.Selection.Mode == "count" {
		count, countErr := strconv.Atoi(decimalString(side.Selection.Value))
		if countErr != nil || count <= 0 || strings.Contains(decimalString(side.Selection.Value), ".") {
			return fmt.Errorf("strategy manifest %s.selection.value must be a positive integer", name)
		}
	}
	if side.Selection.Mode == "fraction" && (value.IsNegative() || value.IsZero() || value.Cmp(quant.One()) > 0) {
		return fmt.Errorf("strategy manifest %s.selection.value fraction must be greater than 0 and at most 1", name)
	}
	return nil
}

func parseDecimalValue(value any) (quant.Decimal, error) {
	return quant.Parse(decimalString(value))
}

func decimalString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func sideWeight(raw string) quant.Decimal {
	value, err := quant.Parse(raw)
	if err != nil {
		return quant.Zero()
	}
	return value
}

func allSpot(markets []string) bool {
	if len(markets) == 0 {
		return false
	}
	for _, market := range markets {
		if !strings.EqualFold(strings.TrimSpace(market), "spot") {
			return false
		}
	}
	return true
}

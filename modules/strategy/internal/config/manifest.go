package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/robfig/cron"
	"gopkg.in/yaml.v3"
)

var frequencyPattern = regexp.MustCompile(`^([1-9][0-9]*)([mhdwMHDW])$`)

// DSL is the user-authored strategy definition.  The shape is deliberately
// small: the rule name is the only dynamic part of the document.
type DSL struct {
	Name string `yaml:"name"`
	// Deprecated compatibility metadata. New DSL must leave these empty.
	APIVersion string          `yaml:"api_version,omitempty"`
	Kind       string          `yaml:"kind,omitempty"`
	Triggers   Triggers        `yaml:"triggers"`
	Data       Data            `yaml:"data"`
	Rules      map[string]Rule `yaml:"rules"`
}

const (
	APIVersion = "moox.strategy/v2"
	Kind       = "coin_selection"
)

type Triggers struct {
	Schedule *Schedule `yaml:"schedule,omitempty"`
	Event    *Event    `yaml:"event,omitempty"`
}

type Schedule struct {
	Cron     string `yaml:"cron"`
	Timezone string `yaml:"timezone"`
	Every    string `yaml:"every,omitempty"` // legacy adapter only
}

type Event struct {
	Name string `yaml:"name"`
}

type Data struct {
	Bar      string `yaml:"bar"`
	Calendar string `yaml:"calendar"`
}

// InstrumentPoolRule is retained for the storage adapter's metadata filter.
// New DSL rules use Pool (a literal list or UDF); this type is an internal
// compatibility shape for callers that still apply exchange/market metadata
// constraints before resolving a Pool.
type InstrumentPoolRule struct {
	Exchanges   []string
	Markets     []string
	QuoteAssets []string
	Include     []string
	// IncludeSet distinguishes an explicit empty fixed pool (select nothing)
	// from an omitted include constraint (select every subject). It is an
	// internal loader hint and is not part of the user DSL.
	IncludeSet bool
	// HistoricalInclude contains IDs carried forward from prior RuleState.
	// They are read when available so exit signals can be evaluated, but a
	// temporarily absent/inactive historical subject must not make the whole
	// current pool invalid.
	HistoricalInclude []string
	Exclude           []string
	MinHistoryPeriods int
}

// Manifest and its helper types are kept only as source adapters for old
// embedders while the persisted contract is DSL. They are not accepted by
// Parse and are never written to the current schema.
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
	Output   string `yaml:"output,omitempty"`
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

// Pool is either a literal instrument list or a registered, trusted UDF.
// Fixed is non-nil for an explicitly supplied list, including an empty list.
type Pool struct {
	Fixed []string
	UDF   *PoolUDF
}

type PoolUDF struct {
	Name   string
	Params map[string]any
}

type Rule struct {
	Pool         Pool     `yaml:"pool"`
	FilterBefore string   `yaml:"filter_before,omitempty"`
	Score        string   `yaml:"score,omitempty"`
	Select       *Select  `yaml:"select,omitempty"`
	Signals      *Signals `yaml:"signals,omitempty"`
	Weight       string   `yaml:"weight,omitempty"`
	WeightEach   string   `yaml:"weight_each,omitempty"`
	Side         string   `yaml:"side,omitempty"`
	FilterAfter  string   `yaml:"filter_after,omitempty"`
	Holding      *Holding `yaml:"holding,omitempty"`
}

type Select struct {
	Where string
	Top   *int
	Tail  *int
}

type Signals struct {
	Entry string
	Exit  string
}

type Holding struct {
	Bars    int
	Offsets []int
}

// Parse parses exactly one YAML document and validates the complete DSL.  We
// parse through a yaml.Node first so duplicate keys in arbitrary UDF params
// are rejected as well as duplicate keys in typed fields.
func Parse(raw []byte) (DSL, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return DSL{}, fmt.Errorf("parse strategy DSL: %w", err)
	}
	if err := rejectDuplicateKeys(&document); err != nil {
		return DSL{}, fmt.Errorf("parse strategy DSL: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return DSL{}, errors.New("strategy DSL must contain one YAML document")
		}
		return DSL{}, fmt.Errorf("parse strategy DSL: %w", err)
	}
	var dsl DSL
	if err := document.Decode(&dsl); err != nil {
		return DSL{}, fmt.Errorf("parse strategy DSL: %w", err)
	}
	if err := Validate(&dsl); err != nil {
		return DSL{}, err
	}
	return dsl, nil
}

func Validate(dsl *DSL) error {
	if dsl == nil {
		return errors.New("strategy DSL is required")
	}
	dsl.Name = strings.TrimSpace(dsl.Name)
	if strings.TrimSpace(dsl.APIVersion) != "" || strings.TrimSpace(dsl.Kind) != "" {
		return errors.New("strategy DSL must not contain api_version or kind")
	}
	if dsl.Name == "" {
		return errors.New("strategy DSL name is required")
	}
	if dsl.Triggers.Schedule == nil && dsl.Triggers.Event == nil {
		return errors.New("strategy DSL requires triggers.schedule or triggers.event")
	}
	if dsl.Triggers.Schedule != nil {
		dsl.Triggers.Schedule.Cron = strings.TrimSpace(dsl.Triggers.Schedule.Cron)
		dsl.Triggers.Schedule.Timezone = strings.TrimSpace(dsl.Triggers.Schedule.Timezone)
		if dsl.Triggers.Schedule.Cron == "" {
			return errors.New("strategy DSL triggers.schedule.cron is required")
		}
		if dsl.Triggers.Schedule.Timezone == "" {
			dsl.Triggers.Schedule.Timezone = "UTC"
		}
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
		if _, err := parser.Parse(dsl.Triggers.Schedule.Cron); err != nil {
			return fmt.Errorf("strategy DSL triggers.schedule.cron: %w", err)
		}
		if _, err := time.LoadLocation(dsl.Triggers.Schedule.Timezone); err != nil {
			return fmt.Errorf("strategy DSL triggers.schedule.timezone: %w", err)
		}
	}
	if dsl.Triggers.Event != nil {
		dsl.Triggers.Event.Name = strings.TrimSpace(dsl.Triggers.Event.Name)
		if dsl.Triggers.Event.Name == "" {
			return errors.New("strategy DSL triggers.event.name is required")
		}
		if !supportedEventName(dsl.Triggers.Event.Name) {
			return fmt.Errorf("strategy DSL triggers.event.name %q is not supported", dsl.Triggers.Event.Name)
		}
	}
	dsl.Data.Bar = strings.TrimSpace(dsl.Data.Bar)
	dsl.Data.Calendar = strings.TrimSpace(dsl.Data.Calendar)
	if dsl.Data.Bar == "" {
		return errors.New("strategy DSL data.bar is required")
	}
	bar, err := normalizeFrequency(dsl.Data.Bar)
	if err != nil {
		return fmt.Errorf("strategy DSL data.bar: %w", err)
	}
	dsl.Data.Bar = bar
	if dsl.Data.Calendar == "" {
		return errors.New("strategy DSL data.calendar is required")
	}
	switch calendar := strings.ToLower(dsl.Data.Calendar); calendar {
	case "crypto_24x7":
		// Continuous markets support the configured minute/hour/day bar.
	case "cn_stock":
		if dsl.Data.Bar != "1D" {
			return fmt.Errorf("strategy DSL data.calendar cn_stock only supports 1d bars")
		}
	default:
		return fmt.Errorf("strategy DSL data.calendar %q is unsupported", dsl.Data.Calendar)
	}
	if len(dsl.Rules) == 0 {
		return errors.New("strategy DSL rules is required")
	}
	for name, rule := range dsl.Rules {
		if strings.TrimSpace(name) == "" {
			return errors.New("strategy DSL rule name must not be empty")
		}
		if err := validateRule(name, &rule); err != nil {
			return err
		}
		dsl.Rules[name] = rule
	}
	return validateWeightBudget(dsl.Rules)
}

func normalizeFrequency(value string) (string, error) {
	matches := frequencyPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(matches) != 3 {
		return "", fmt.Errorf("frequency %q is invalid", value)
	}
	unit := matches[2]
	// In the report package lowercase m means minutes while uppercase M means
	// months. Preserve that distinction and reject month/week units for the
	// first implementation, whose calendar contract is minute/hour/day only.
	if unit == "M" || unit == "w" || unit == "W" {
		return "", fmt.Errorf("frequency %q is unsupported; use minute/hour/day", value)
	}
	if unit != "m" {
		unit = strings.ToUpper(unit)
	}
	return matches[1] + unit, nil
}

func supportedEventName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "viewfactorperiodready", "factor.ready", "event.storage.view.factor_period.ready", "viewsourceperiodready", "ready", "source.ready", "event.storage.view.source_period.ready":
		return true
	default:
		return false
	}
}

func validateRule(name string, rule *Rule) error {
	if rule == nil {
		return fmt.Errorf("strategy DSL rules.%s is required", name)
	}
	if err := validatePool(name, &rule.Pool); err != nil {
		return err
	}
	rule.FilterBefore = strings.TrimSpace(rule.FilterBefore)
	rule.Score = strings.TrimSpace(rule.Score)
	rule.FilterAfter = strings.TrimSpace(rule.FilterAfter)
	if rule.Side == "" {
		rule.Side = "long"
	}
	if rule.Side != "long" && rule.Side != "short" {
		return fmt.Errorf("strategy DSL rules.%s.side must be long or short", name)
	}
	if (strings.TrimSpace(rule.Weight) == "") == (strings.TrimSpace(rule.WeightEach) == "") {
		return fmt.Errorf("strategy DSL rules.%s requires exactly one of weight or weight_each", name)
	}
	for field, raw := range map[string]string{"weight": rule.Weight, "weight_each": rule.WeightEach} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		value, err := quant.Parse(strings.TrimSpace(raw))
		if err != nil || value.IsNegative() || value.IsZero() || value.Cmp(quant.One()) > 0 {
			return fmt.Errorf("strategy DSL rules.%s.%s must be greater than 0 and at most 1", name, field)
		}
	}
	if rule.Select != nil {
		if strings.TrimSpace(rule.Score) == "" {
			return fmt.Errorf("strategy DSL rules.%s.select requires score", name)
		}
		if strings.TrimSpace(rule.Select.Where) == "" && rule.Select.Top == nil && rule.Select.Tail == nil {
			return fmt.Errorf("strategy DSL rules.%s.select must configure where, top or tail", name)
		}
		if rule.Select.Top != nil && rule.Select.Tail != nil {
			return fmt.Errorf("strategy DSL rules.%s.select.top and tail are mutually exclusive", name)
		}
		if rule.Select.Top != nil && *rule.Select.Top <= 0 {
			return fmt.Errorf("strategy DSL rules.%s.select.top must be positive", name)
		}
		if rule.Select.Tail != nil && *rule.Select.Tail <= 0 {
			return fmt.Errorf("strategy DSL rules.%s.select.tail must be positive", name)
		}
		rule.Select.Where = strings.TrimSpace(rule.Select.Where)
	}
	if rule.Signals != nil {
		rule.Signals.Entry = strings.TrimSpace(rule.Signals.Entry)
		rule.Signals.Exit = strings.TrimSpace(rule.Signals.Exit)
		if rule.Signals.Entry == "" || rule.Signals.Exit == "" {
			return fmt.Errorf("strategy DSL rules.%s.signals requires entry and exit", name)
		}
	}
	if rule.Holding != nil {
		if rule.Signals != nil {
			return fmt.Errorf("strategy DSL rules.%s cannot combine holding and signals", name)
		}
		if rule.Holding.Bars <= 0 {
			return fmt.Errorf("strategy DSL rules.%s.holding.bars must be positive", name)
		}
		if len(rule.Holding.Offsets) == 0 {
			return fmt.Errorf("strategy DSL rules.%s.holding.offsets is required", name)
		}
		if strings.TrimSpace(rule.WeightEach) != "" {
			return fmt.Errorf("strategy DSL rules.%s.holding requires weight, not weight_each", name)
		}
		seen := make(map[int]struct{}, len(rule.Holding.Offsets))
		for _, offset := range rule.Holding.Offsets {
			if offset < 0 || offset >= rule.Holding.Bars {
				return fmt.Errorf("strategy DSL rules.%s.holding.offsets contains out-of-range value", name)
			}
			if _, exists := seen[offset]; exists {
				return fmt.Errorf("strategy DSL rules.%s.holding.offsets contains duplicate value", name)
			}
			seen[offset] = struct{}{}
		}
	}
	return nil
}

// validateWeightBudget rejects configurations whose maximum declared
// allocation can exceed one. A dynamic UDF pool has no statically knowable
// cardinality, so it must use weight (which is split across the actual
// selected rows) rather than weight_each.
func validateWeightBudget(rules map[string]Rule) error {
	total := quant.Zero()
	for name, rule := range rules {
		if strings.TrimSpace(rule.Weight) != "" {
			value, err := quant.Parse(strings.TrimSpace(rule.Weight))
			if err != nil {
				return fmt.Errorf("strategy DSL rules.%s.weight: %w", name, err)
			}
			total = total.Add(value)
			continue
		}
		if rule.Pool.Fixed == nil {
			return fmt.Errorf("strategy DSL rules.%s.weight_each requires a fixed pool with known size", name)
		}
		count := len(rule.Pool.Fixed)
		if rule.Select != nil && rule.Signals == nil {
			if rule.Select.Top != nil && *rule.Select.Top < count {
				count = *rule.Select.Top
			}
			if rule.Select.Tail != nil && *rule.Select.Tail < count {
				count = *rule.Select.Tail
			}
		}
		each, err := quant.Parse(strings.TrimSpace(rule.WeightEach))
		if err != nil {
			return fmt.Errorf("strategy DSL rules.%s.weight_each: %w", name, err)
		}
		total = total.Add(each.Mul(quant.Must(strconv.Itoa(count))))
	}
	if total.Cmp(quant.One()) > 0 {
		return fmt.Errorf("strategy DSL rule weight upper bound exceeds 1: %s", total.String())
	}
	return nil
}

func validatePool(name string, pool *Pool) error {
	if pool == nil || (pool.Fixed == nil && pool.UDF == nil) {
		return fmt.Errorf("strategy DSL rules.%s.pool is required", name)
	}
	if pool.Fixed != nil && pool.UDF != nil {
		return fmt.Errorf("strategy DSL rules.%s.pool must be a list or udf", name)
	}
	if pool.Fixed != nil {
		seen := make(map[string]struct{}, len(pool.Fixed))
		for i, value := range pool.Fixed {
			value = strings.TrimSpace(value)
			if value == "" {
				return fmt.Errorf("strategy DSL rules.%s.pool[%d] is empty", name, i)
			}
			key := strings.ToUpper(value)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("strategy DSL rules.%s.pool contains duplicate %q", name, value)
			}
			seen[key] = struct{}{}
			pool.Fixed[i] = value
		}
		return nil
	}
	pool.UDF.Name = strings.TrimSpace(pool.UDF.Name)
	if pool.UDF.Name == "" {
		return fmt.Errorf("strategy DSL rules.%s.pool.udf is required", name)
	}
	return nil
}

func parseScalarText(node *yaml.Node, field string) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!int" && node.Tag != "!!float") {
		return "", fmt.Errorf("%s must be a string or number", field)
	}
	return strings.TrimSpace(node.Value), nil
}

func decodeRequiredText(node *yaml.Node, field string) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s must be a string", field)
	}
	value := strings.TrimSpace(node.Value)
	if value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	return value, nil
}

func decodePositiveInt(node *yaml.Node, field string) (int, error) {
	text, err := parseScalarText(node, field)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(text)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}
	return value, nil
}

func rejectDuplicateKeys(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			if err := rejectDuplicateKeys(child); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("duplicate field %q", key.Value)
			}
			seen[key.Value] = struct{}{}
			if err := rejectDuplicateKeys(node.Content[i+1]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := rejectDuplicateKeys(child); err != nil {
			return err
		}
	}
	return nil
}

func (d *DSL) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("strategy DSL must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "name":
			text, err := decodeRequiredText(value, "name")
			if err != nil {
				return err
			}
			d.Name = text
		case "triggers":
			if err := value.Decode(&d.Triggers); err != nil {
				return fmt.Errorf("triggers: %w", err)
			}
		case "data":
			if err := value.Decode(&d.Data); err != nil {
				return fmt.Errorf("data: %w", err)
			}
		case "rules":
			if value.Kind != yaml.MappingNode {
				return errors.New("rules must be a mapping")
			}
			d.Rules = make(map[string]Rule, len(value.Content)/2)
			for j := 0; j < len(value.Content); j += 2 {
				name := value.Content[j].Value
				var rule Rule
				if err := value.Content[j+1].Decode(&rule); err != nil {
					return fmt.Errorf("rules.%s: %w", name, err)
				}
				d.Rules[name] = rule
			}
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

func (t *Triggers) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("triggers must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "schedule":
			var schedule Schedule
			if err := value.Decode(&schedule); err != nil {
				return fmt.Errorf("schedule: %w", err)
			}
			t.Schedule = &schedule
		case "event":
			var event Event
			if err := value.Decode(&event); err != nil {
				return fmt.Errorf("event: %w", err)
			}
			t.Event = &event
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

func (s *Schedule) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("schedule must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		text, err := decodeRequiredText(value, "schedule."+key.Value)
		if err != nil {
			return err
		}
		switch key.Value {
		case "cron":
			s.Cron = text
		case "timezone":
			s.Timezone = text
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

func (e *Event) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("event must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Value != "name" {
			return fmt.Errorf("unknown field %q", key.Value)
		}
		text, err := decodeRequiredText(value, "event.name")
		if err != nil {
			return err
		}
		e.Name = text
	}
	return nil
}

func (d *Data) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("data must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		text, err := decodeRequiredText(value, "data."+key.Value)
		if err != nil {
			return err
		}
		switch key.Value {
		case "bar":
			d.Bar = text
		case "calendar":
			d.Calendar = text
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

func (p *Pool) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		p.Fixed = make([]string, len(node.Content))
		for i, item := range node.Content {
			text, err := decodeRequiredText(item, fmt.Sprintf("pool[%d]", i))
			if err != nil {
				return err
			}
			p.Fixed[i] = text
		}
		return nil
	case yaml.MappingNode:
		var udf PoolUDF
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			switch key.Value {
			case "udf":
				text, err := decodeRequiredText(value, "pool.udf")
				if err != nil {
					return err
				}
				udf.Name = text
			case "params":
				if value.Kind != yaml.MappingNode {
					return errors.New("pool.params must be a mapping")
				}
				if err := value.Decode(&udf.Params); err != nil {
					return fmt.Errorf("pool.params: %w", err)
				}
			default:
				return fmt.Errorf("unknown field %q", key.Value)
			}
		}
		p.UDF = &udf
		return nil
	default:
		return errors.New("pool must be a list or udf mapping")
	}
}

func (r *Rule) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("rule must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "pool":
			if err := value.Decode(&r.Pool); err != nil {
				return fmt.Errorf("pool: %w", err)
			}
		case "filter_before", "score", "filter_after":
			text, err := decodeRequiredText(value, key.Value)
			if err != nil {
				return err
			}
			switch key.Value {
			case "filter_before":
				r.FilterBefore = text
			case "score":
				r.Score = text
			default:
				r.FilterAfter = text
			}
		case "select":
			var selectRule Select
			if err := value.Decode(&selectRule); err != nil {
				return fmt.Errorf("select: %w", err)
			}
			r.Select = &selectRule
		case "signals":
			var signals Signals
			if err := value.Decode(&signals); err != nil {
				return fmt.Errorf("signals: %w", err)
			}
			r.Signals = &signals
		case "weight", "weight_each":
			text, err := parseScalarText(value, key.Value)
			if err != nil {
				return err
			}
			if key.Value == "weight" {
				r.Weight = text
			} else {
				r.WeightEach = text
			}
		case "side":
			text, err := decodeRequiredText(value, "side")
			if err != nil {
				return err
			}
			r.Side = text
		case "holding":
			var holding Holding
			if err := value.Decode(&holding); err != nil {
				return fmt.Errorf("holding: %w", err)
			}
			r.Holding = &holding
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

func (s *Select) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("select must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "where":
			text, err := decodeRequiredText(value, "select.where")
			if err != nil {
				return err
			}
			s.Where = text
		case "top", "tail":
			count, err := decodePositiveInt(value, "select."+key.Value)
			if err != nil {
				return err
			}
			if key.Value == "top" {
				s.Top = &count
			} else {
				s.Tail = &count
			}
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

func (s *Signals) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("signals must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		text, err := decodeRequiredText(value, "signals."+key.Value)
		if err != nil {
			return err
		}
		switch key.Value {
		case "entry":
			s.Entry = text
		case "exit":
			s.Exit = text
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

func (h *Holding) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("holding must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "bars":
			bars, err := decodePositiveInt(value, "holding.bars")
			if err != nil {
				return err
			}
			h.Bars = bars
		case "offsets":
			if value.Kind != yaml.SequenceNode {
				return errors.New("holding.offsets must be a list")
			}
			h.Offsets = make([]int, len(value.Content))
			for j, item := range value.Content {
				text, err := parseScalarText(item, fmt.Sprintf("holding.offsets[%d]", j))
				if err != nil {
					return err
				}
				offset, err := strconv.Atoi(text)
				if err != nil {
					return fmt.Errorf("holding.offsets[%d] must be an integer", j)
				}
				h.Offsets[j] = offset
			}
		default:
			return fmt.Errorf("unknown field %q", key.Value)
		}
	}
	return nil
}

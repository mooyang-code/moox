package input

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func (in EvaluationInput) Validate() error {
	seen := make(map[string]struct{}, len(in.Items))
	for i, item := range in.Items {
		if item.InstrumentID == "" {
			return fmt.Errorf("evaluation input item %d instrument_id is required", i)
		}
		if _, exists := seen[item.InstrumentID]; exists {
			return fmt.Errorf("evaluation input instrument %q is duplicated", item.InstrumentID)
		}
		seen[item.InstrumentID] = struct{}{}
		for factorID := range item.Values {
			if factorID == "" {
				return fmt.Errorf("evaluation input item %d factor_id is required", i)
			}
		}
	}
	return nil
}

func Hash(in EvaluationInput) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	ordered := in.Ordered()
	type canonicalItem struct {
		InstrumentID string            `json:"instrument_id"`
		SubjectID    string            `json:"subject_id,omitempty"`
		Exchange     string            `json:"exchange,omitempty"`
		Market       string            `json:"market,omitempty"`
		QuoteAsset   string            `json:"quote_asset,omitempty"`
		SeriesTag    string            `json:"series_tag,omitempty"`
		Values       map[string]string `json:"values"`
		Previous     map[string]string `json:"previous,omitempty"`
	}
	payload := struct {
		SpaceID       string          `json:"space_id"`
		StrategyID    string          `json:"strategy_id"`
		PeriodEnd     string          `json:"period_end"`
		SourceViewID  string          `json:"source_view_id,omitempty"`
		DataFrequency string          `json:"data_frequency"`
		Items         []canonicalItem `json:"items"`
	}{SpaceID: in.SpaceID, StrategyID: in.StrategyID, PeriodEnd: in.PeriodEnd, SourceViewID: in.SourceViewID, DataFrequency: in.DataFrequency}
	payload.Items = make([]canonicalItem, 0, len(ordered))
	for _, item := range ordered {
		values := make(map[string]string, len(item.Values))
		for factorID, value := range item.Values {
			values[factorID] = value.String()
		}
		previous := make(map[string]string, len(item.PreviousValues))
		for field, value := range item.PreviousValues {
			previous[field] = value.String()
		}
		payload.Items = append(payload.Items, canonicalItem{InstrumentID: item.InstrumentID, SubjectID: item.SubjectID, Exchange: item.Exchange, Market: item.Market, QuoteAsset: item.QuoteAsset, SeriesTag: item.SeriesTag, Values: values, Previous: previous})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

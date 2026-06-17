package discovery

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/source"
)

func FilterByQuoteAsset(instruments []source.Instrument, quoteAsset string) []source.Instrument {
	quote := strings.ToUpper(strings.TrimSpace(quoteAsset))
	if quote == "" {
		return append([]source.Instrument(nil), instruments...)
	}

	out := make([]source.Instrument, 0, len(instruments))
	for _, instrument := range instruments {
		if strings.ToUpper(instrument.QuoteAsset) == quote {
			out = append(out, instrument)
		}
	}
	return out
}

package marketdata

import "strings"

// CanonicalCryptoSubjectID is the durable data ID for crypto rows.
// Spot and swap share the same BASE-QUOTE identity; instrument type lives
// on the Dataset, not as a subject suffix.
func CanonicalCryptoSubjectID(id string) string {
	value := strings.ToUpper(strings.TrimSpace(id))
	for _, suffix := range []string{"-SPOT", "-SWAP"} {
		value = strings.TrimSuffix(value, suffix)
	}
	return value
}

// CanonicalizeCryptoInstrument rewrites a crypto instrument onto the suffix-free
// subject identity used by Storage rows and later mdataset merges.
func CanonicalizeCryptoInstrument(instrument Instrument) Instrument {
	instrument.SubjectID = CanonicalCryptoSubjectID(instrument.SubjectID)
	return instrument
}

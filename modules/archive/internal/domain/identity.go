package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var monthPattern = regexp.MustCompile(`^[0-9]{6}$`)

type PartitionKey struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	Freq      string `json:"freq"`
	SeriesTag string `json:"series_tag"`
	Month     string `json:"month"`
}

func EncodeIdentity(raw string) string {
	encodeDots := raw == "." || raw == ".."
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		allowed := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
		if encodeDots && c == '.' {
			allowed = false
		}
		if c == '_' && i+1 < len(raw) && raw[i+1] == '_' {
			allowed = false
		}
		if allowed {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func DecodeIdentity(encoded string) (string, error) { return url.PathUnescape(encoded) }

func MonthOf(t time.Time) string { return t.UTC().Format("200601") }

func (k PartitionKey) Validate() error {
	if !stableIDPattern.MatchString(k.SpaceID) || !stableIDPattern.MatchString(k.DatasetID) || !stableIDPattern.MatchString(k.Freq) {
		return fmt.Errorf("invalid partition identity")
	}
	if k.SubjectID == "" || strings.ContainsRune(k.SubjectID, 0) {
		return fmt.Errorf("subject_id is required")
	}
	if err := ValidateSeriesTag(k.SeriesTag); err != nil {
		return err
	}
	if !monthPattern.MatchString(k.Month) {
		return fmt.Errorf("invalid partition month %q", k.Month)
	}
	month, _ := time.Parse("200601", k.Month)
	if month.Month() == 0 {
		return fmt.Errorf("invalid partition month %q", k.Month)
	}
	return nil
}

func ValidateSeriesTag(tag string) error {
	if !utf8.ValidString(tag) {
		return fmt.Errorf("series_tag must be valid UTF-8")
	}
	if len(tag) > 128 {
		return fmt.Errorf("series_tag must not exceed 128 bytes")
	}
	if strings.TrimSpace(tag) != tag {
		return fmt.Errorf("series_tag must not have leading or trailing whitespace")
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] < 0x20 || tag[i] == 0x7f {
			return fmt.Errorf("series_tag must not contain ASCII control characters")
		}
	}
	return nil
}

func (k PartitionKey) FileName() (string, error) {
	if err := k.Validate(); err != nil {
		return "", err
	}
	return strings.Join([]string{
		EncodeIdentity(k.SpaceID),
		EncodeIdentity(k.DatasetID),
		EncodeIdentity(k.SubjectID),
		EncodeIdentity(k.Freq),
		"series_tag=" + EncodeIdentity(k.SeriesTag),
		k.Month,
	}, "__") + ".parquet", nil
}

func (k PartitionKey) RelativePath() (string, error) {
	name, err := k.FileName()
	if err != nil {
		return "", err
	}
	return filepath.Join(
		EncodeIdentity(k.SpaceID),
		EncodeIdentity(k.DatasetID),
		EncodeIdentity(k.Freq),
		EncodeIdentity(k.SubjectID),
		"series_tag="+EncodeIdentity(k.SeriesTag),
		name,
	), nil
}

func (k PartitionKey) AbsolutePath(root string) (string, error) {
	rel, err := k.RelativePath()
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(base, rel)
	check, err := filepath.Rel(base, abs)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive path escapes root")
	}
	return abs, nil
}

func ParseFileName(name string) (PartitionKey, error) {
	if !strings.HasSuffix(name, ".parquet") {
		return PartitionKey{}, fmt.Errorf("not a parquet archive filename")
	}
	parts := strings.Split(strings.TrimSuffix(name, ".parquet"), "__")
	if len(parts) != 6 {
		return PartitionKey{}, fmt.Errorf("archive v2 filename must contain six fields including series_tag")
	}
	space, err := DecodeIdentity(parts[0])
	if err != nil {
		return PartitionKey{}, err
	}
	dataset, err := DecodeIdentity(parts[1])
	if err != nil {
		return PartitionKey{}, err
	}
	subject, err := DecodeIdentity(parts[2])
	if err != nil {
		return PartitionKey{}, err
	}
	freq, err := DecodeIdentity(parts[3])
	if err != nil {
		return PartitionKey{}, err
	}
	if !strings.HasPrefix(parts[4], "series_tag=") {
		return PartitionKey{}, fmt.Errorf("archive v2 filename is missing series_tag")
	}
	tagEncoded := strings.TrimPrefix(parts[4], "series_tag=")
	tag, err := DecodeIdentity(tagEncoded)
	if err != nil {
		return PartitionKey{}, err
	}
	if EncodeIdentity(tag) != tagEncoded {
		return PartitionKey{}, fmt.Errorf("non-canonical series_tag encoding")
	}
	k := PartitionKey{SpaceID: space, DatasetID: dataset, SubjectID: subject, Freq: freq, SeriesTag: tag, Month: parts[5]}
	if err := k.Validate(); err != nil {
		return PartitionKey{}, err
	}
	return k, nil
}

func PartitionID(k PartitionKey) string {
	return digest(strings.Join([]string{k.SpaceID, k.DatasetID, k.Freq, k.SubjectID, k.SeriesTag, k.Month}, "\n"))
}
func LogicalRowID(t time.Time) string {
	return digest(fmt.Sprintf("%d", t.UTC().UnixNano()))
}
func digest(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }

func ParseArchivePath(path string) (PartitionKey, error) {
	key, err := ParseFileName(filepath.Base(path))
	if err != nil {
		return PartitionKey{}, err
	}
	canonicalName, err := key.FileName()
	if err != nil {
		return PartitionKey{}, err
	}
	if filepath.Base(path) != canonicalName {
		return PartitionKey{}, fmt.Errorf("archive filename is not canonically encoded")
	}
	expected := []string{
		EncodeIdentity(key.SpaceID),
		EncodeIdentity(key.DatasetID),
		EncodeIdentity(key.Freq),
		EncodeIdentity(key.SubjectID),
		"series_tag=" + EncodeIdentity(key.SeriesTag),
	}
	dir := filepath.Dir(filepath.Clean(path))
	actual := make([]string, len(expected))
	for i := len(actual) - 1; i >= 0; i-- {
		base := filepath.Base(dir)
		if base == "." || base == string(filepath.Separator) || base == "" {
			return PartitionKey{}, fmt.Errorf("archive v2 path is missing partition directories")
		}
		actual[i] = base
		dir = filepath.Dir(dir)
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return PartitionKey{}, fmt.Errorf("archive path parent identity mismatch: got %q, want %q", actual[i], expected[i])
		}
	}
	return key, nil
}

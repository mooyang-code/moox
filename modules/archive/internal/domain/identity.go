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
)

var stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var monthPattern = regexp.MustCompile(`^[0-9]{6}$`)

type PartitionKey struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	Freq      string `json:"freq"`
	Month     string `json:"month"`
}

func EncodeIdentity(raw string) string {
	if raw == "." || raw == ".." {
		raw = strings.ReplaceAll(raw, ".", "%2E")
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		allowed := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
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
	if !monthPattern.MatchString(k.Month) {
		return fmt.Errorf("invalid partition month %q", k.Month)
	}
	month, _ := time.Parse("200601", k.Month)
	if month.Month() == 0 {
		return fmt.Errorf("invalid partition month %q", k.Month)
	}
	return nil
}

func (k PartitionKey) FileName() (string, error) {
	if err := k.Validate(); err != nil {
		return "", err
	}
	return strings.Join([]string{EncodeIdentity(k.SpaceID), EncodeIdentity(k.DatasetID), EncodeIdentity(k.SubjectID), EncodeIdentity(k.Freq), k.Month}, "__") + ".parquet", nil
}

func (k PartitionKey) RelativePath() (string, error) {
	name, err := k.FileName()
	if err != nil {
		return "", err
	}
	return filepath.Join(EncodeIdentity(k.SpaceID), EncodeIdentity(k.DatasetID), EncodeIdentity(k.Freq), EncodeIdentity(k.SubjectID), name), nil
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
	if len(parts) != 5 {
		return PartitionKey{}, fmt.Errorf("archive filename must contain five fields")
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
	k := PartitionKey{SpaceID: space, DatasetID: dataset, SubjectID: subject, Freq: freq, Month: parts[4]}
	if err := k.Validate(); err != nil {
		return PartitionKey{}, err
	}
	return k, nil
}

func PartitionID(k PartitionKey) string {
	return digest(strings.Join([]string{k.SpaceID, k.DatasetID, k.SubjectID, k.Freq, k.Month}, "\n"))
}
func LogicalRowID(t time.Time, dimensionsJSON string) string {
	return digest(fmt.Sprintf("%d\n%s", t.UTC().UnixNano(), dimensionsJSON))
}
func digest(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }

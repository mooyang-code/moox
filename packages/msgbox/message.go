package msgbox

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	maxBodyCharacters       = 4096
	maxLabels               = 16
	maxLabelKeyCharacters   = 64
	maxLabelValueCharacters = 256
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Message struct {
	Key      string
	Severity Severity
	Title    string
	Body     string
	Labels   map[string]string
}

func (m Message) validate() error {
	switch m.Severity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
	default:
		return errors.New("msgbox: invalid message severity")
	}
	if utf8.RuneCountInString(m.Body) > maxBodyCharacters {
		return fmt.Errorf("msgbox: message body exceeds %d character limit", maxBodyCharacters)
	}
	if len(m.Labels) > maxLabels {
		return fmt.Errorf("msgbox: labels exceed %d item limit", maxLabels)
	}
	for key, value := range m.Labels {
		if utf8.RuneCountInString(key) > maxLabelKeyCharacters {
			return fmt.Errorf("msgbox: label key exceeds %d character limit", maxLabelKeyCharacters)
		}
		if utf8.RuneCountInString(value) > maxLabelValueCharacters {
			return fmt.Errorf("msgbox: label value exceeds %d character limit", maxLabelValueCharacters)
		}
	}
	return nil
}

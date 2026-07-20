package doctor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	core "github.com/mooyang-code/moox/packages/doctor"
)

func Render(report core.Report, format string) ([]byte, error) {
	switch format {
	case "json":
		return report.MarshalJSONBounded()
	case "text":
		return renderText(report), nil
	case "markdown":
		return renderMarkdown(report), nil
	default:
		return nil, fmt.Errorf("unsupported doctor format %q", format)
	}
}

func WriteAtomic(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("output path is required")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".moox-doctor-report-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func renderText(report core.Report) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "MooX Doctor %s\n", report.Conclusion)
	fmt.Fprintf(&out, "run: %s\nmode: %s\n", report.RunID, report.Mode)
	for _, check := range report.Checks {
		fmt.Fprintf(&out, "[%s] %s: %s\n", check.Status, check.ID, check.Summary)
	}
	return out.Bytes()
}

func renderMarkdown(report core.Report) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "# MooX Doctor: %s\n\n", report.Conclusion)
	fmt.Fprintf(&out, "- Run: `%s`\n- Mode: `%s`\n\n", report.RunID, report.Mode)
	out.WriteString("| Status | Check | Summary |\n| --- | --- | --- |\n")
	for _, check := range report.Checks {
		summary := strings.ReplaceAll(check.Summary, "|", "\\|")
		fmt.Fprintf(&out, "| %s | `%s` | %s |\n", check.Status, check.ID, summary)
	}
	return out.Bytes()
}

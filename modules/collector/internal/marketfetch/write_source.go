package marketfetch

import "strings"

// writeSourceForFunction gives Storage events a stable, extensible source
// value without coupling the public event contract to Tencent SCF forever.
func writeSourceForFunctionName(functionName string) string {
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		return ""
	}
	return "scf:" + functionName
}

func functionNameFromWriteSource(source string) string {
	source = strings.TrimSpace(source)
	if !strings.HasPrefix(source, "scf:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(source, "scf:"))
}

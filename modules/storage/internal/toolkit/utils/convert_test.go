package utils

import (
	"testing"
)

func TestEncodeSymbol(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "normal alphanumeric",
			input:    "ABC123_def",
			expected: "ABC123_def",
		},
		{
			name:     "with dash",
			input:    "ARB-USDT",
			expected: "ARB_x2D_USDT",
		},
		{
			name:     "with special characters",
			input:    "test@#$",
			expected: "test_x40__x23__x24_",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only special characters",
			input:    "-@#",
			expected: "_x2D__x40__x23_",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EncodeSymbol(tt.input)
			if result != tt.expected {
				t.Errorf("EncodeSymbol(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestDecodeSymbol(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "normal alphanumeric",
			input:    "ABC123_def",
			expected: "ABC123_def",
		},
		{
			name:     "with encoded dash",
			input:    "ARB_x2D_USDT",
			expected: "ARB-USDT",
		},
		{
			name:     "with encoded special characters",
			input:    "test_x40__x23__x24_",
			expected: "test@#$",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only encoded special characters",
			input:    "_x2D__x40__x23_",
			expected: "-@#",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DecodeSymbol(tt.input)
			if err != nil {
				t.Errorf("DecodeSymbol(%q) returned error: %v", tt.input, err)
				return
			}
			if result != tt.expected {
				t.Errorf("DecodeSymbol(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEscapeTableIDDash(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no dash",
			input:    "ARB_USDT",
			expected: "ARB_USDT",
		},
		{
			name:     "single dash",
			input:    "ARB-USDT",
			expected: "ARB_x2D_USDT",
		},
		{
			name:     "multiple dashes",
			input:    "ARB-USDT-BTC",
			expected: "ARB_x2D_USDT_x2D_BTC",
		},
		{
			name:     "dash at start",
			input:    "-USDT",
			expected: "_x2D_USDT",
		},
		{
			name:     "dash at end",
			input:    "ARB-",
			expected: "ARB_x2D_",
		},
		{
			name:     "only dash",
			input:    "-",
			expected: "_x2D_",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "with other special chars",
			input:    "ARB-USDT@123",
			expected: "ARB_x2D_USDT@123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EscapeTableIDDash(tt.input)
			if result != tt.expected {
				t.Errorf("EscapeTableIDDash(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestUnescapeTableIDDash(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no escaped dash",
			input:    "ARB_USDT",
			expected: "ARB_USDT",
		},
		{
			name:     "single escaped dash",
			input:    "ARB_x2D_USDT",
			expected: "ARB-USDT",
		},
		{
			name:     "multiple escaped dashes",
			input:    "ARB_x2D_USDT_x2D_BTC",
			expected: "ARB-USDT-BTC",
		},
		{
			name:     "escaped dash at start",
			input:    "_x2D_USDT",
			expected: "-USDT",
		},
		{
			name:     "escaped dash at end",
			input:    "ARB_x2D_",
			expected: "ARB-",
		},
		{
			name:     "only escaped dash",
			input:    "_x2D_",
			expected: "-",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UnescapeTableIDDash(tt.input)
			if result != tt.expected {
				t.Errorf("UnescapeTableIDDash(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEscapeUnescapeTableIDDashRoundTrip(t *testing.T) {
	tests := []string{
		"ARB-USDT",
		"ARB-USDT-BTC",
		"-USDT",
		"ARB-",
		"-",
		"",
		"ARB_USDT",
		"ARB-USDT@123",
		"test-with-multiple-dashes",
	}

	for _, original := range tests {
		t.Run(original, func(t *testing.T) {
			escaped := EscapeTableIDDash(original)
			unescaped := UnescapeTableIDDash(escaped)
			if unescaped != original {
				t.Logf("Original:  %q", original)
				t.Logf("Escaped:   %q", escaped)
				t.Logf("Unescaped: %q", unescaped)
				t.Errorf("Round trip failed: %q -> %q -> %q", original, escaped, unescaped)
			}
		})
	}
}

func TestParseDataTableParts(t *testing.T) {
	tests := []struct {
		name         string
		tableID      string
		wantDataset  int32
		wantObjectID string
		wantFreq     string
		wantErr      bool
	}{
		{
			name:         "object with underscores and freq",
			tableID:      GenDataTableID(101, "BTC_USDT", "1H"),
			wantDataset:  101,
			wantObjectID: "BTC_USDT",
			wantFreq:     "1H",
		},
		{
			name:         "unicode object and freq",
			tableID:      GenDataTableID(101, "中文", "1H"),
			wantDataset:  101,
			wantObjectID: "中文",
			wantFreq:     "1H",
		},
		{
			name:        "freq without object id",
			tableID:     "t_data_101_1H",
			wantDataset: 101,
			wantFreq:    "1H",
			wantErr:     true,
		},
		{
			name:    "invalid prefix",
			tableID: "data_101_BTC_USDT_1H",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			datasetID, objectID, freq, err := ParseDataTableParts(tt.tableID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDataTableParts(%q) returned nil error", tt.tableID)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDataTableParts(%q) returned error: %v", tt.tableID, err)
			}
			if datasetID != tt.wantDataset {
				t.Fatalf("datasetID = %d, want %d", datasetID, tt.wantDataset)
			}
			if objectID != tt.wantObjectID {
				t.Fatalf("objectID = %q, want %q", objectID, tt.wantObjectID)
			}
			if freq != tt.wantFreq {
				t.Fatalf("freq = %q, want %q", freq, tt.wantFreq)
			}
		})
	}
}

func TestParseDataTableObjectID(t *testing.T) {
	tests := []struct {
		name      string
		tableID   string
		expected  string
		shouldErr bool
	}{
		{
			name:     "simple object id",
			tableID:  "t_data_101_BTC_1H",
			expected: "BTC",
		},
		{
			name:     "encoded dash",
			tableID:  "t_data_101_ARB_x2D_USDT_1H",
			expected: "ARB-USDT",
		},
		{
			name:     "encoded unicode",
			tableID:  "t_data_101__xE4__xB8__xAD__xE6__x96__x87_1H",
			expected: "中文",
		},
		{
			name:     "object with underscore",
			tableID:  "t_data_101_ABC_DEF_1H",
			expected: "ABC_DEF",
		},
		{
			name:      "missing object id",
			tableID:   "t_data_101_1H",
			shouldErr: true,
		},
		{
			name:      "not data table",
			tableID:   "t_object_101",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseDataTableObjectID(tt.tableID)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("ParseDataTableObjectID(%q) expected error but got none", tt.tableID)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseDataTableObjectID(%q) returned error: %v", tt.tableID, err)
				return
			}
			if result != tt.expected {
				t.Errorf("ParseDataTableObjectID(%q) = %q, want %q", tt.tableID, result, tt.expected)
			}
		})
	}
}

func TestParseDataTableFreq(t *testing.T) {
	tests := []struct {
		name      string
		tableID   string
		expected  string
		shouldErr bool
	}{
		{
			name:     "simple freq",
			tableID:  "t_data_101_BTC_1H",
			expected: "1H",
		},
		{
			name:      "missing freq",
			tableID:   "t_data_101_BTC",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseDataTableFreq(tt.tableID)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("ParseDataTableFreq(%q) expected error but got none", tt.tableID)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseDataTableFreq(%q) returned error: %v", tt.tableID, err)
				return
			}
			if result != tt.expected {
				t.Errorf("ParseDataTableFreq(%q) = %q, want %q", tt.tableID, result, tt.expected)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tests := []string{
		"ARB-USDT",
		"test@#$%^&*()",
		"normal_text_123",
		"中文测试",
		"",
		"mix中文-and@symbols",
	}

	for _, original := range tests {
		t.Run(original, func(t *testing.T) {
			encoded := EncodeSymbol(original)
			decoded, err := DecodeSymbol(encoded)
			if err != nil {
				t.Errorf("DecodeSymbol failed for %q: %v", encoded, err)
				return
			}
			if decoded != original {
				t.Logf("Original: %q", original)
				t.Logf("Encoded:  %q", encoded)
				t.Logf("Decoded:  %q", decoded)
				t.Errorf("Round trip failed: %q -> %q -> %q", original, encoded, decoded)
			}
		})
	}
}

func TestGenDataTableID(t *testing.T) {
	tests := []struct {
		name      string
		datasetID int32
		objectID  string
		freq      string
		expected  string
	}{
		{
			name:      "normal case",
			datasetID: 101,
			objectID:  "ARB-USDT",
			freq:      "1H",
			expected:  "t_data_101_ARB_x2D_USDT_1H",
		},
		{
			name:      "zero dataset ID",
			datasetID: 0,
			objectID:  "test",
			freq:      "1D",
			expected:  "t_data_test_1D",
		},
		{
			name:      "empty object ID",
			datasetID: 101,
			objectID:  "",
			freq:      "1H",
			expected:  "t_data_101_1H",
		},
		{
			name:      "empty freq",
			datasetID: 101,
			objectID:  "test",
			freq:      "",
			expected:  "t_data_101_test",
		},
		{
			name:      "all empty except dataset",
			datasetID: 101,
			objectID:  "",
			freq:      "",
			expected:  "t_data_101",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenDataTableID(tt.datasetID, tt.objectID, tt.freq)
			if result != tt.expected {
				t.Errorf("GenDataTableID(%d, %q, %q) = %q, want %q",
					tt.datasetID, tt.objectID, tt.freq, result, tt.expected)
			}
		})
	}
}

func TestParseDatasetIDFromTableID(t *testing.T) {
	tests := []struct {
		name        string
		tableID     string
		expectedID  int32
		expectError bool
	}{
		{
			name:        "t_data normal case with all parts",
			tableID:     "t_data_101_ARB_x2D_USDT_1H",
			expectedID:  101,
			expectError: false,
		},
		{
			name:        "t_data only dataset ID",
			tableID:     "t_data_101",
			expectedID:  101,
			expectError: false,
		},
		{
			name:        "t_data zero dataset ID",
			tableID:     "t_data",
			expectedID:  0,
			expectError: false,
		},
		{
			name:        "t_data dataset with object but no freq",
			tableID:     "t_data_101_test",
			expectedID:  101,
			expectError: false,
		},
		{
			name:        "t_data large dataset ID",
			tableID:     "t_data_999999_test_1D",
			expectedID:  999999,
			expectError: false,
		},
		{
			name:        "t_object with dataset ID",
			tableID:     "t_object_101",
			expectedID:  101,
			expectError: false,
		},
		{
			name:        "t_object without dataset ID",
			tableID:     "t_object",
			expectedID:  0,
			expectError: false,
		},
		{
			name:        "t_object large dataset ID",
			tableID:     "t_object_999999",
			expectedID:  999999,
			expectError: false,
		},
		{
			name:        "invalid prefix",
			tableID:     "t_invalid_101",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "missing underscore after prefix",
			tableID:     "t_data101",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "invalid dataset ID format",
			tableID:     "t_data_abc_test",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "empty string",
			tableID:     "",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "negative dataset ID",
			tableID:     "t_data_-1_test",
			expectedID:  -1,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseDatasetIDFromTableID(tt.tableID)

			if tt.expectError {
				if err == nil {
					t.Errorf("ParseDatasetIDFromTableID(%q) expected error but got none", tt.tableID)
				}
				return
			}

			if err != nil {
				t.Errorf("ParseDatasetIDFromTableID(%q) unexpected error: %v", tt.tableID, err)
				return
			}

			if result != tt.expectedID {
				t.Errorf("ParseDatasetIDFromTableID(%q) = %d, want %d", tt.tableID, result, tt.expectedID)
			}
		})
	}
}

package picocache

import "testing"

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		// Valid GB
		{"1GB", 1073741824, false},
		{"1 GB", 1073741824, false},
		{"1gb", 1073741824, false},
		{"2GB", 2147483648, false},

		// Valid MB
		{"500MB", 524288000, false},
		{"500 MB", 524288000, false},
		{"500mb", 524288000, false},
		{"1MB", 1048576, false},

		// Floating point
		{"1.5GB", 1610612736, false},
		{"1.5MB", 1572864, false},
		{"0.5GB", 536870912, false},

		// Edge cases
		{"  1GB  ", 1073741824, false},
		{"1  GB", 1073741824, false},

		// Invalid inputs
		{"", 0, true},
		{"GB", 0, true},
		{"123", 0, true},
		{"1TB", 0, true},
		{"-1GB", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseSize(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

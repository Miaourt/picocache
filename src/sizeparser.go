package picocache

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	gigabyte = 1024 * megabyte
	megabyte = 1024 * 1024
)

func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	var numStr string
	var multiplier int64
	if strings.HasSuffix(s, "GB") {
		numStr, multiplier = strings.TrimSpace(s[:len(s)-2]), gigabyte
	} else if strings.HasSuffix(s, "MB") {
		numStr, multiplier = strings.TrimSpace(s[:len(s)-2]), megabyte
	} else {
		return 0, fmt.Errorf("unsupported unit, use MB or GB")
	}

	value, err := strconv.ParseFloat(numStr, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid size value: %s", numStr)
	}

	return int64(value * float64(multiplier)), nil
}

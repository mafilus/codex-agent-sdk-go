//go:build integration

package integration

import (
	"strings"
)

// contains42 checks if a string mentions the number 42.
func contains42(s string) bool {
	lower := strings.ToLower(s)

	return strings.Contains(lower, "42") ||
		strings.Contains(lower, "forty-two") ||
		strings.Contains(lower, "forty two")
}

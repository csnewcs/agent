package main

import (
	"math"
	"testing"
	"time"
)

func TestParseDateString(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)

	testCases := []struct {
		input       string
		expectedY   int
		expectedM   time.Month
		expectedD   int
		expectError bool
	}{
		{"2025.05.10", 2025, time.May, 10, false},
		{"2025-05-10", 2025, time.May, 10, false},
		{"2025/05/10", 2025, time.May, 10, false},
		{"20250510", 2025, time.May, 10, false},
		{"2025. 5. 10", 2025, time.May, 10, false},
		{"2025-5-10", 2025, time.May, 10, false},
		{"invalid-date", 0, 0, 0, true},
	}

	for _, tc := range testCases {
		res, err := parseDateString(tc.input, loc)
		if tc.expectError {
			if err == nil {
				t.Errorf("expected error for %q, got nil", tc.input)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.input, err)
			}
			if res.Year() != tc.expectedY || res.Month() != tc.expectedM || res.Day() != tc.expectedD {
				t.Errorf("parseDateString(%q) = %v, expected %d-%d-%d", tc.input, res, tc.expectedY, tc.expectedM, tc.expectedD)
			}
		}
	}
}

func TestDDayCalculation(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	baseDate := time.Date(2025, time.May, 10, 0, 0, 0, 0, loc)
	targetToday := time.Date(2026, time.September, 1, 0, 0, 0, 0, loc)

	diff := targetToday.Sub(baseDate)
	days := int(math.Round(diff.Hours() / 24))

	if days != 479 {
		t.Errorf("expected 479 days between 2025-05-10 and 2026-09-01, got %d", days)
	}
}

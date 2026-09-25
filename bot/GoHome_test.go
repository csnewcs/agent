package main

import (
	"testing"
	"time"
)

func TestGetOffWorkTarget(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)

	testCases := []struct {
		name       string
		time       time.Time
		expectOk   bool
		expectHour int
		expectMin  int
	}{
		{
			name:       "Monday 17:30",
			time:       time.Date(2026, 8, 31, 10, 0, 0, 0, loc), // Monday
			expectOk:   true,
			expectHour: 17,
			expectMin:  30,
		},
		{
			name:       "Tuesday 17:00",
			time:       time.Date(2026, 9, 1, 10, 0, 0, 0, loc), // Tuesday
			expectOk:   true,
			expectHour: 17,
			expectMin:  0,
		},
		{
			name:       "Wednesday 17:30",
			time:       time.Date(2026, 9, 2, 10, 0, 0, 0, loc), // Wednesday
			expectOk:   true,
			expectHour: 17,
			expectMin:  30,
		},
		{
			name:       "Thursday 17:00",
			time:       time.Date(2026, 9, 3, 10, 0, 0, 0, loc), // Thursday
			expectOk:   true,
			expectHour: 17,
			expectMin:  0,
		},
		{
			name:       "Friday 17:30",
			time:       time.Date(2026, 9, 4, 10, 0, 0, 0, loc), // Friday
			expectOk:   true,
			expectHour: 17,
			expectMin:  30,
		},
		{
			name:     "Saturday Off",
			time:     time.Date(2026, 9, 5, 10, 0, 0, 0, loc), // Saturday
			expectOk: false,
		},
		{
			name:     "Sunday Off",
			time:     time.Date(2026, 9, 6, 10, 0, 0, 0, loc), // Sunday
			expectOk: false,
		},
		{
			name:     "Holiday Off (Chuseok)",
			time:     time.Date(2026, 9, 25, 10, 0, 0, 0, loc), // Friday Chuseok
			expectOk: false,
		},
	}

	for _, tc := range testCases {
		target, ok := getOffWorkTarget(tc.time, loc)
		if ok != tc.expectOk {
			t.Errorf("[%s] expected ok=%v, got %v", tc.name, tc.expectOk, ok)
			continue
		}
		if tc.expectOk {
			if target.Hour() != tc.expectHour || target.Minute() != tc.expectMin {
				t.Errorf("[%s] expected %02d:%02d, got %02d:%02d", tc.name, tc.expectHour, tc.expectMin, target.Hour(), target.Minute())
			}
		}
	}
}

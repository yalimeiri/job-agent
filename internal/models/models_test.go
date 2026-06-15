package models

import "testing"

func TestJobStatusValid(t *testing.T) {
	tests := []struct {
		status JobStatus
		want   bool
	}{
		{StatusNew, true},
		{StatusMatched, true},
		{StatusRejected, true},
		{StatusApplying, true},
		{StatusApplied, true},
		{StatusFailed, true},
		{StatusSkipped, true},
		{JobStatus("bogus"), false},
		{JobStatus(""), false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.want {
			t.Errorf("%q.Valid() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

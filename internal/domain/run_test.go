package domain

import "testing"

func TestRunStatus_Terminal(t *testing.T) {
	cases := map[RunStatus]bool{
		StatusApplied:  true,
		StatusErrored:  true,
		StatusCanceled: true,
		StatusDiscarded: true,
		StatusPlanning: false,
		StatusPending:  false,
		StatusPlanned:  false,
		StatusApplying: false,
	}
	for s, want := range cases {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}

func TestRunStatus_Active(t *testing.T) {
	cases := map[RunStatus]bool{
		StatusPlanning:       true,
		StatusApplying:       true,
		StatusFetching:       true,
		StatusCostEstimating: true,
		StatusPolicyChecking: true,
		StatusPending:        false,
		StatusPlanned:        false,
		StatusApplied:        false,
	}
	for s, want := range cases {
		if got := s.Active(); got != want {
			t.Errorf("%s.Active() = %v, want %v", s, got, want)
		}
	}
}

func TestCanTransitionTo(t *testing.T) {
	tests := []struct {
		from, to RunStatus
		want     bool
	}{
		{StatusPending, StatusPlanning, true},
		{StatusPending, StatusFetching, true},
		{StatusPending, StatusCanceled, true},
		{StatusPlanning, StatusPlanned, true},
		{StatusPlanning, StatusErrored, true},
		{StatusPlanning, StatusCanceled, true},
		{StatusPlanned, StatusApplying, true},
		{StatusPlanned, StatusDiscarded, true},
		{StatusApplying, StatusApplied, true},

		{StatusApplied, StatusPlanning, false},
		{StatusErrored, StatusPlanning, false},
		{StatusCanceled, StatusPending, false},

		{StatusPending, StatusApplied, false},
		{StatusPlanning, StatusApplying, false},
		{StatusPending, StatusApplying, false},
	}

	for _, tc := range tests {
		if got := tc.from.CanTransitionTo(tc.to); got != tc.want {
			t.Errorf("%s -> %s: got %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

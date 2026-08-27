package backup

import "testing"

func TestPercentOf_ClampsToOneWhenCumulativeExceedsTotal(t *testing.T) {
	// A running VM's snapshot adds delta/.vmsn files to the bundle after
	// totalBytes is measured, so the Copying stage can see more
	// cumulative bytes than totalBytes accounted for.
	got := percentOf(150, 100)
	if got != 1 {
		t.Errorf("percentOf(150, 100) = %v, want 1", got)
	}
}

func TestPercentOf_ClampsToOneWhenCumulativeEqualsTotal(t *testing.T) {
	got := percentOf(100, 100)
	if got != 1 {
		t.Errorf("percentOf(100, 100) = %v, want 1", got)
	}
}

func TestPercentOf_ReturnsZeroWhenTotalIsZero(t *testing.T) {
	got := percentOf(0, 0)
	if got != 0 {
		t.Errorf("percentOf(0, 0) = %v, want 0", got)
	}
}

func TestPercentOf_ReturnsFractionWithinRange(t *testing.T) {
	got := percentOf(25, 100)
	if got != 0.25 {
		t.Errorf("percentOf(25, 100) = %v, want 0.25", got)
	}
}

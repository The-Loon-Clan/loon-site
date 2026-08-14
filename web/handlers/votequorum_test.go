package handlers

import (
	"fmt"
	"testing"
)

// What a tally of votes means.
//
// communitymod_web.go was 126 of 126 statements uncovered — the whole file —
// and this is the part of it that decides whether somebody's picture comes
// down because other members said so. The arithmetic is four lines and every
// boundary in it is a judgement somebody argued for in a comment: three votes
// before sentiment counts, two thirds rather than a bare half, and a split
// left open rather than resolved the cheap way.
//
// Boundaries are exactly where this kind of rule goes wrong, and the wrongness
// is quiet: a threshold off by one still produces plausible outcomes, just not
// the ones anybody agreed to.

func TestBelowQuorumNothingIsDecided(t *testing.T) {
	// The point of a quorum: two people who feel strongly are not the
	// community, however lopsided their two votes look.
	for _, tc := range []struct{ removes, keeps int }{
		{0, 0}, {1, 0}, {2, 0}, {0, 2}, {1, 1},
	} {
		if got := voteOutcome(tc.removes, tc.keeps); got != "" {
			t.Errorf("%d remove / %d keep decided %q with fewer than %d votes",
				tc.removes, tc.keeps, got, voteQuorum)
		}
	}
}

func TestQuorumIsReachedAtExactlyThreeVotes(t *testing.T) {
	// Inclusive, and worth pinning: the difference between >= and > here is
	// the difference between the documented rule and one vote more than it.
	if got := voteOutcome(3, 0); got != resolutionRemoved {
		t.Errorf("3 unanimous remove votes gave %q, want %q — quorum is %d and it is inclusive",
			got, resolutionRemoved, voteQuorum)
	}
	if got := voteOutcome(2, 0); got != "" {
		t.Errorf("2 votes decided %q; quorum is %d", got, voteQuorum)
	}
}

func TestTwoThirdsCarriesAndABareMajorityDoesNot(t *testing.T) {
	// The stated rule: removing somebody's picture on a 3-2 split is a decision
	// the losing half will reasonably call arbitrary. A simple majority would
	// resolve every one of the "no decision" rows below.
	for _, tc := range []struct {
		removes, keeps int
		want           string
		why            string
	}{
		{3, 0, resolutionRemoved, "unanimous"},
		{4, 2, resolutionRemoved, "exactly two thirds"},
		{5, 2, resolutionRemoved, "above two thirds"},
		{3, 2, "", "a bare majority is not enough"},
		{4, 3, "", "4 of 7 is a majority and not two thirds"},
		{0, 3, resolutionKept, "unanimous keep"},
		{2, 4, resolutionKept, "exactly two thirds keep"},
		{2, 3, "", "a bare majority to keep is not enough either"},
	} {
		got := voteOutcome(tc.removes, tc.keeps)
		if got != tc.want {
			t.Errorf("%d remove / %d keep = %q, want %q (%s)",
				tc.removes, tc.keeps, got, tc.want, tc.why)
		}
	}
}

func TestTheRuleIsSymmetric(t *testing.T) {
	// Keeping and removing are held to the same bar. An asymmetry here would
	// mean the queue quietly favours one outcome, which is precisely the thing
	// a community vote is supposed to take out of any one person's hands.
	for removes := 0; removes <= 12; removes++ {
		for keeps := 0; keeps <= 12; keeps++ {
			forward := voteOutcome(removes, keeps)
			mirrored := voteOutcome(keeps, removes)

			var want string
			switch forward {
			case resolutionRemoved:
				want = resolutionKept
			case resolutionKept:
				want = resolutionRemoved
			default:
				want = ""
			}
			if mirrored != want {
				t.Fatalf("%d/%d gives %q but %d/%d gives %q, want %q — the rule is not symmetric",
					removes, keeps, forward, keeps, removes, mirrored, want)
			}
		}
	}
}

func TestASplitStaysOpen(t *testing.T) {
	// A tie at or above quorum is a real answer, and the item stays for staff
	// or for more votes. Resolving it either way would be the code picking a
	// side the community did not.
	for _, n := range []int{2, 3, 4, 5, 10} {
		if got := voteOutcome(n, n); got != "" {
			t.Errorf("an even split %d/%d resolved as %q", n, n, got)
		}
	}
}

func TestMoreVotesCanOnlyStrengthenAnOutcome(t *testing.T) {
	// Monotonicity. Adding a remove vote must never turn a "remove" into a
	// "keep", and must never un-decide a decided removal — a rule that flips
	// under an extra vote is one where the last voter decides the outcome by
	// accident.
	for keeps := 0; keeps <= 10; keeps++ {
		for removes := 0; removes <= 10; removes++ {
			before := voteOutcome(removes, keeps)
			after := voteOutcome(removes+1, keeps)

			if before == resolutionRemoved && after != resolutionRemoved {
				t.Errorf("%d/%d was %q; one more remove vote made it %q",
					removes, keeps, before, after)
			}
			if before == resolutionKept && after == resolutionRemoved {
				t.Errorf("%d/%d was %q; one more remove vote flipped it straight to %q",
					removes, keeps, before, after)
			}
		}
	}
}

func TestTheThresholdMatchesTheConstantsRatherThanACopyOfThem(t *testing.T) {
	// Derived from voteQuorum and the majority fraction, so tuning those — the
	// numbers the file says a real deployment tunes — does not leave this test
	// asserting the old policy while the code follows the new one.
	q := voteQuorum
	for total := q; total <= q+9; total++ {
		for removes := 0; removes <= total; removes++ {
			keeps := total - removes
			want := ""
			switch {
			case removes*voteMajorityDen >= total*voteMajorityNum:
				want = resolutionRemoved
			case keeps*voteMajorityDen >= total*voteMajorityNum:
				want = resolutionKept
			}
			if got := voteOutcome(removes, keeps); got != want {
				t.Fatalf("%d remove / %d keep = %q, want %q by the constants",
					removes, keeps, got, want)
			}
		}
	}
}

func TestNoNegativeOrNonsenseTallyPanics(t *testing.T) {
	// The tally comes from a COUNT, so it cannot be negative today. This is
	// here so that if it ever comes from somewhere else, the failure is a
	// wrong answer rather than a panic in the middle of casting a vote.
	for _, tc := range []struct{ removes, keeps int }{{-1, 5}, {5, -1}, {-3, -3}} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("voteOutcome(%d, %d) panicked: %v", tc.removes, tc.keeps, r)
				}
			}()
			_ = voteOutcome(tc.removes, tc.keeps)
		}()
	}
}

// Example_voteOutcome documents the rule in the form somebody actually asks it:
// "we have five votes, four of them to remove — what happens?"
func Example_voteOutcome() {
	fmt.Println("3/0 ->", voteOutcome(3, 0))
	fmt.Println("4/2 ->", voteOutcome(4, 2))
	fmt.Println("3/2 ->", voteOutcome(3, 2) == "")
	fmt.Println("1/1 ->", voteOutcome(1, 1) == "")
	// Output:
	// 3/0 -> removed
	// 4/2 -> removed
	// 3/2 -> true
	// 1/1 -> true
}

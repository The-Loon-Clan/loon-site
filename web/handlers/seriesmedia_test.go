package handlers

import (
	"context"
	"log/slog"
	"io"
	"strings"
	"testing"
)

// stubSummaries stands in for the mediainfo plugin's capability.
type stubSummaries struct{ out map[int64]string }

func (s stubSummaries) SummariesFor(context.Context, []int64) (map[int64]string, error) {
	return s.out, nil
}

// The measured line is MEMBER-SUBMITTED, and nothing upstream bounds one field
// of it: the plugin caps the whole paste (64KB) and the LINE COUNT, so a single
// "Format :" line of sixty thousand unbroken characters parses, stores, and
// arrives here whole.
//
// Unbounded, that one paste widens the series table for every reader of the
// show — the line is nowrap inside a flex row that cannot shrink below
// max-content, and the table has no fixed layout — and the hover rule that
// lifts overflow containment then scrolls the entire document sideways. One
// member, every reader, no error anywhere.
//
// Bounded HERE rather than in CSS alone, because the point is that the string
// never reaches the DOM, not that it is hidden once there.
func TestReleaseSummariesBoundsMemberText(t *testing.T) {
	huge := strings.Repeat("A", 60000) + " at 10.4 Mb/s"
	w := &web{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		mediaSummaries: stubSummaries{out: map[int64]string{7: huge}},
	}

	got := w.releaseSummaries(context.Background(), []int64{7})[7]
	// mediaSummaryMax counts CONTENT runes; Ellipsis appends one more to mark
	// the cut, so the rendered ceiling is one above it. Asserted precisely
	// rather than loosely — writing this test is what showed the constant does
	// not mean quite what its name suggests on its own.
	if n := len([]rune(got)); n > mediaSummaryMax+1 {
		t.Errorf("summary came through at %d runes, want <= %d — one paste widens "+
			"the table for every reader of the show", n, mediaSummaryMax+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated summary = %q, want a trailing ellipsis so the cut is visible", got)
	}
}

// A summary already inside the bound must arrive untouched — a cap that
// silently trims the ordinary case would make every real line end in an
// ellipsis that means nothing.
func TestReleaseSummariesLeavesOrdinaryLinesAlone(t *testing.T) {
	const real = "HEVC at 10.4 Mb/s · E-AC-3 JOC 6 channels"
	w := &web{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		mediaSummaries: stubSummaries{out: map[int64]string{7: real}},
	}
	if got := w.releaseSummaries(context.Background(), []int64{7})[7]; got != real {
		t.Errorf("summary = %q, want it unchanged at %q", got, real)
	}
}

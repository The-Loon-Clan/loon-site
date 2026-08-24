package handlers

import "testing"

// humanSize renders bytes at the scale a release listing reads at.
func TestHumanSizeReadsAtReleaseScale(t *testing.T) {
	cases := map[int64]string{
		2147483648: "2.0 GB",
		514986736:  "491 MB",
		1024:       "1024 B",
	}
	for b, want := range cases {
		if got := humanSize(b); got != want {
			t.Fatalf("humanSize(%d) = %q, want %q", b, got, want)
		}
	}
}

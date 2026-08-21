"""Does sqllint actually catch an injection?

SECURITY.md claimed "the linter is tested against a real injection rather than
only against a clean tree". It was not. There was no test of any kind — the
linter ran over a clean repository, found nothing, and that was read as proof
it worked. A linter that has never refused anything and a linter that cannot
refuse anything produce identical output on a clean tree.

That is the whole reason this file exists. Each case below is written to a
temporary .go file and scanned; BAD cases must be reported and GOOD cases must
not. A change that breaks the detector now fails here instead of going
unnoticed until something is exploited.

Run: python scripts/sqllint_selftest.py   (make check runs it before sqllint)
"""

import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sqllint  # noqa: E402

HEADER = "package x\n\nimport \"context\"\n\nvar db X\ntype X struct{}\n\n"

# (label, source, must_be_flagged)
CASES = [
    # ── the real thing: a user-controlled value concatenated into a statement.
    ("concatenated user input", '''
func Bad(ctx context.Context, name string) {
	db.QueryContext(ctx, "SELECT id FROM users WHERE name = '" + name + "'")
}
''', True),

    ("fmt.Sprintf into a statement", '''
func Bad2(ctx context.Context, table string) {
	q := fmt.Sprintf("DELETE FROM %s WHERE id = $1", table)
	db.ExecContext(ctx, q)
}
''', True),

    ("concatenated ORDER BY, the classic 'it is only a column name'", '''
func Bad3(ctx context.Context, col string) {
	db.QueryContext(ctx, "SELECT id FROM releases ORDER BY " + col)
}
''', True),

    ("built across several lines with +=", '''
func Bad4(ctx context.Context, where string) {
	q := "SELECT id FROM users"
	q += " WHERE " + where
	db.QueryContext(ctx, q)
}
''', True),

    # ── the shapes that must NOT be flagged, or the linter is unusable and
    # gets switched off, which is the other way a guard stops working.
    ("a plain constant statement", '''
func Good(ctx context.Context, name string) {
	db.QueryContext(ctx, `SELECT id FROM users WHERE name = $1`, name)
}
''', False),

    ("a package-level const", '''
const qUser = `SELECT id FROM users WHERE id = $1`

func Good2(ctx context.Context, id int64) {
	db.QueryContext(ctx, qUser, id)
}
''', False),

    ("an explicit allow, with its reason", '''
func Good3(ctx context.Context, table string) {
	db.ExecContext(ctx, "DELETE FROM " + table) // sqllint:allow the names are literals in a fixed slice
}
''', False),
]


def run_case(src):
    """Scan one snippet and return the findings."""
    fd, path = tempfile.mkstemp(suffix=".go", prefix="sqllint_selftest_")
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as fh:
            fh.write(HEADER + src)
        # scan() takes a pathlib.Path in sqllint.main; a str works the same
        # for open(), but use the same type the caller does.
        from pathlib import Path
        return sqllint.scan(Path(path))
    finally:
        os.unlink(path)


def main():
    failures = 0
    for label, src, must_flag in CASES:
        findings = run_case(src)
        flagged = len(findings) > 0
        if flagged != must_flag:
            failures += 1
            want = "flagged" if must_flag else "allowed"
            got = "flagged" if flagged else "allowed"
            print("FAIL  %-52s want %s, got %s" % (label, want, got))
            for _, ln, why, text in findings:
                print("        line %d: %s" % (ln, why))
        else:
            print("ok    %-52s %s" % (label, "flagged" if flagged else "allowed"))

    if failures:
        print("\nsqllint self-test: %d of %d cases wrong.\n"
              "The linter is the injection guard; if it cannot refuse the bad\n"
              "cases above, a clean run over the repository proves nothing."
              % (failures, len(CASES)))
        return 1
    print("\nsqllint self-test: %d cases, the detector refuses what it must "
          "and allows what it must" % len(CASES))
    return 0


if __name__ == "__main__":
    sys.exit(main())

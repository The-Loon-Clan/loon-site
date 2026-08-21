#!/usr/bin/env python3
"""Refuse SQL that is assembled from anything but constants.

    python scripts/sqllint.py            # whole tree, exits non-zero on a finding
    python scripts/sqllint.py --list     # show every SQL site it can see

What it looks for
-----------------
Parameterised queries are safe no matter what a visitor types, because the
values never become part of the statement. The way that protection is lost is
always the same: a fragment gets CONCATENATED or FORMATTED into the SQL text
instead of passed as a parameter. So this flags exactly that:

  1. `... WHERE ` + x          — string concatenation into a SQL literal
  2. fmt.Sprintf("SELECT ...") — a format string producing SQL
  3. db.Query(q) / Exec(q)     — a statement handed over as a plain variable

and then, for (1) and (3), tries to prove the fragment is a compile-time
constant. A `const where = "..."`, a literal, or a variable assigned only
literals is fine — those cannot carry user input by construction. Anything it
cannot prove is reported.

This codebase passes today, and that is deliberate: the four places that build
SQL by concatenation all splice a constant chosen by a bool
(`where := "a"; if mine { where = "b" }`), which is a safe pattern that looks
exactly like an unsafe one at a glance. A reviewer cannot tell them apart
without following each identifier; this does that following.

Escape hatch
------------
    // sqllint:allow <reason>

on the offending line or the line above. A reason is REQUIRED — an unexplained
suppression is the failure mode this tool exists to prevent, so the reason is
part of the syntax rather than a convention.

Not a parser
------------
It reads text, not an AST, so it is deliberately conservative: it would rather
ask about a safe line than stay quiet about an unsafe one. If it becomes noisy
the fix is a narrower rule, never a broader allow.
"""
import re
import sys
from pathlib import Path

SQL_KEYWORD = re.compile(r'\b(SELECT|INSERT\s+INTO|UPDATE|DELETE\s+FROM|CREATE\s+TABLE|ALTER\s+TABLE|DROP\s+TABLE)\b',
                         re.IGNORECASE)
ALLOW = re.compile(r'//\s*sqllint:allow\s+(\S.*)')

# db.Query("..."), tx.ExecContext(ctx, "..."), and friends.
CALL = re.compile(r'\b(?:Query|QueryRow|Exec|Select|Get)(?:Context)?\s*\(')

SKIP_DIRS = {'.git', 'node_modules', '__pycache__', 'examples', 'docs'}

# What makes a DOUBLE-QUOTED literal look like SQL rather than prose.
#
# Stated here rather than inline because these are the patterns that went
# wrong: one carried a literal backspace (0x08) where a \\b was meant, so it
# could never match and the rule built on it was dead for its whole life.
# A stray byte is visible on a line of its own next to its neighbours; it is
# not visible buried in the middle of an if-expression.
#
# Two tests, because a bare clause word is not evidence -- "Gift from " + to
# is a ledger description and it matches FROM. Either the literal OPENS with
# a statement verb, or it carries a two-word clause marker that English does
# not produce by accident.
OPENS_A_STATEMENT = r'"\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|WITH)\b'
STATEMENT_SHAPE = (r'\b(INSERT\s+INTO|DELETE\s+FROM|ORDER\s+BY|GROUP\s+BY|'
                  r'LEFT\s+JOIN|INNER\s+JOIN)\b|\sWHERE\s|\sSET\s|\sVALUES\s*\(')


def allowed(lines, i):
    """True when this line carries an explicit, reasoned suppression."""
    for probe in (lines[i], lines[i - 1] if i else ''):
        m = ALLOW.search(probe)
        if m and m.group(1).strip():
            return True
    return False


def const_names(src):
    """Identifiers this file proves are constant strings.

    Covers `const x = "..."`, `x := "..."` and reassignment `x = "..."` where
    EVERY assignment is a literal — the bool-chosen-fragment pattern this
    codebase uses. An identifier assigned anything else is not included, which
    is the point: it might carry a request value.
    """
    # A right-hand side counts as constant only if it is WHOLLY a literal.
    #
    # The first version tested rhs.startswith('`'), which called
    #
    #     where := `w.title LIKE '%` + userSearch + `%'`
    #
    # a constant because it begins with a backtick — and then rule 1 trusted
    # `WHERE ` + where + ` and let a textbook injection through. That exact
    # line is the linter's own test case now; see the note at the bottom of
    # this file.
    # A literal, or a literal wrapped in the SQL type.
    #
    # `where := storage.SQL(\`w.filled_at IS NULL\`)` is a constant that has
    # also been TYPED, and typing it is strictly stronger than not: the
    # compiler then refuses any later assignment of a runtime string to it. So
    # this form is at least as safe as a bare literal, and the linter defers to
    # the type system rather than duplicating its judgement.
    PURE = re.compile(r'^(?:(?:storage\.)?SQL\()?(?:`[^`]*`|"(?:[^"\\]|\\.)*")\)?\s*$')
    assigned = {}

    # Multi-line raw strings first, over the whole file. A predicate held as
    #
    #     const pendingAvatarWhere = `avatar_path <> ''
    #         AND avatar_reviewed_at IS NULL`
    #
    # is as constant as a one-liner, but a per-line test sees an unterminated
    # backtick on the first line and concludes it is not — which flagged six
    # correct constants and would have taught everyone to ignore the tool.
    for m in re.finditer(r'(?m)^\s*(?:const\s+)?([a-zA-Z_]\w*)\s*(?::=|=)\s*`[^`]*`\s*$', src, re.S):
        assigned.setdefault(m.group(1), []).append(True)

    for m in re.finditer(r'(?m)^\s*(?:const\s+)?([a-zA-Z_]\w*)\s*(?::=|=)\s*(.+)$', src):
        name, rhs = m.group(1), m.group(2).strip()
        if name in assigned and assigned[name] == [True] and rhs.startswith('`') and rhs.count('`') == 1:
            continue  # the opening line of the multi-line literal handled above
        assigned.setdefault(name, []).append(bool(PURE.match(rhs)))
    # multi-assignment: where, order := `a`, `b`
    for m in re.finditer(r'(?m)^\s*([a-zA-Z_]\w*(?:\s*,\s*[a-zA-Z_]\w*)+)\s*(?::=|=)\s*(.+)$', src):
        names = [n.strip() for n in m.group(1).split(',')]
        parts = [p.strip() for p in m.group(2).split(',')]
        if len(names) == len(parts):
            for n, p in zip(names, parts):
                assigned.setdefault(n, []).append(bool(PURE.match(p)))
    return {n for n, kinds in assigned.items() if kinds and all(kinds)}


def blank_comments(src):
    """Comments replaced by spaces, keeping every offset and line number.

    The rules must run over CODE. Without this the tool flagged its own
    documentation — the lines in internal/storage/conn.go that quote
    `WHERE ` + someConst and SQL(x) in order to explain them — which is the
    fourth time in this project that prose mentioning an identifier has been
    read as a use of it.
    """
    out = list(src)
    i, n = 0, len(src)
    while i < n:
        if src.startswith('//', i):
            j = src.find('\n', i)
            j = n if j == -1 else j
            for k in range(i, j):
                out[k] = ' '
            i = j
        elif src.startswith('/*', i):
            j = src.find('*/', i + 2)
            j = n if j == -1 else j + 2
            for k in range(i, j):
                if out[k] != '\n':
                    out[k] = ' '
            i = j
        else:
            i += 1
    return ''.join(out)


def scan(path):
    raw = path.read_text(encoding='utf-8', errors='replace').replace('\r\n', '\n')
    src = blank_comments(raw)
    if not SQL_KEYWORD.search(src):
        return []
    lines = raw.split('\n')  # originals, for display and for the allow-check
    code = src.split('\n')   # comments blanked: what the per-line rules read
    consts = const_names(src)
    out = []

    # conn.go is the wrapper itself. Its whole job is to make the one
    # unavoidable conversion — string(q), from an SQL-typed parameter — in a
    # single reviewed place, so that nowhere else has to. Linting it for doing
    # that is linting the guard for standing at the gate.
    if path.name == 'conn.go' and 'storage' in path.parts:
        return []

    # 1. concatenation into a SQL string, scanned over the WHOLE file.
    #
    # Line by line does not work here and quietly failed to: this project's
    # statements are multi-line raw strings, so the interesting line reads
    #
    #        WHERE ` + where + `
    #
    # where those backticks CLOSE the literal above and REOPEN it below. A
    # per-line regex sees a self-contained `...` and finds nothing to complain
    # about, which is exactly what it did while a textbook injection sat in
    # front of it.
    # BOTH QUOTE STYLES. This was backtick-only for a long time, on the
    # reasoning that this project's statements are multi-line raw strings. That
    # holds inside internal/storage, where Conn's methods take the named type
    # SQL and a `string` expression will not compile at all -- the compiler is
    # the guard there, not this. It does NOT hold in the twenty-odd call sites
    # outside it that use a raw sqlx handle, where "SELECT ... " + name is
    # ordinary Go that builds and runs.
    for m in re.finditer(r'([`"])\s*\+\s*([a-zA-Z_][\w.]*)', src):
        quote, frag = m.group(1), m.group(2)
        if frag.split('.')[0] in consts:
            continue
        # Is this string SQL at all? The two quote styles need DIFFERENT
        # evidence, and using one test for both is what produced false
        # positives the moment this was widened past backticks:
        #
        #     {fromID, -amount, fromBal, "Gift to " + to}   a parameter VALUE
        #     key := "rewards.payout." + k                  a registry key
        #
        # Both sit within 400 characters of a statement, because ordinary
        # strings live next to SQL all the time.
        if quote == '`':
            # A raw string here IS a statement, split across lines, so the
            # fragment beside the + often carries no keyword of its own:
            #
            #        WHERE ` + where + `
            #
            # The neighbourhood is the only evidence there is. Requiring a SQL
            # word nearby keeps the rule off `attachment; filename="` + name —
            # a Content-Disposition header with no database near it.
            evidence = src[max(0, m.start() - 400):m.start()]
        else:
            # A double-quoted string is usually not SQL, so it has to say so
            # itself: walk back to its opening quote and look only in there.
            #
            # And a bare clause word is not enough evidence. "Gift from " + to
            # is a LEDGER DESCRIPTION, and it matched FROM — English uses these
            # words. So a double-quoted splice must look like a STATEMENT:
            # opening with a verb, or carrying a two-word clause marker that
            # prose does not produce by accident.
            start = src.rfind('"', 0, m.start())
            lit = src[start:m.start()] if start >= 0 else ''
            if not re.match(OPENS_A_STATEMENT, lit, re.IGNORECASE) and \
               not re.search(STATEMENT_SHAPE, lit, re.IGNORECASE):
                continue
            evidence = lit
        if not re.search(r'\b(SELECT|INSERT|UPDATE|DELETE|FROM|WHERE|JOIN|VALUES|SET|ORDER\s+BY)\b',
                         evidence, re.IGNORECASE):
            continue
        line_no = src.count('\n', 0, m.start()) + 1
        if allowed(lines, line_no - 1):
            continue
        out.append((path, line_no, 'concatenated into a SQL string: %s' % frag,
                    lines[line_no - 1].strip()))

    for i, line in enumerate(code):
        if allowed(lines, i):
            continue

        # 2. an explicit conversion to the SQL type
        #
        # internal/storage.SQL makes a built statement a COMPILE error, so the
        # only way past it is to say so out loud: storage.SQL(x). That cannot
        # be removed from the language and should not be — sqlx.In legitimately
        # hands back a string it expanded itself. But it is the one remaining
        # door, so it is the one this tool now watches, and it needs a reason.
        for m in re.finditer(r'\b(?:storage\.)?SQL\(([^)]*)\)', line):
            arg = m.group(1).strip()
            if arg.startswith('`') or arg.startswith('"'):
                continue  # converting a literal changes nothing
            out.append((path, i + 1,
                        'explicit conversion to SQL: %s — the compiler cannot check this one' % arg,
                        line.strip()))

        # 3. a format string that builds SQL
        #
        # The format must OPEN with a SQL verb. Matching the keyword anywhere
        # flagged a human-readable fix message that happened to contain the
        # word "select", which is the sort of noise that gets a linter switched
        # off rather than obeyed.
        if 'Sprintf(' in line:
            after = line[line.index('Sprintf(') + len('Sprintf('):].lstrip()
            # A VERB IS NOT ENOUGH. This rule carried a stray backspace in its
            # pattern for its whole life, so it matched nothing at all; the
            # moment that was fixed it flagged
            #
            #     fmt.Sprintf("select users.%s in the user_display view ...")
            #
            # a human-readable message — exactly the noise the note above
            # predicted, arriving the instant the rule could speak. A statement
            # has structure as well as a verb, so the format must carry a
            # second SQL word too.
            if after[:1] in ('`', '"') and \
               re.match(r'[`"]\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|WITH)\b',
                        after, re.IGNORECASE) and \
               re.search(r'\b(FROM|WHERE|SET|VALUES|INTO|TABLE|JOIN|RETURNING)\b',
                         after, re.IGNORECASE):
                out.append((path, i + 1, 'fmt.Sprintf builds SQL', line.strip()))

        # 3. a statement handed over as a bare variable
        #
        # A call carrying a string LITERAL is writing its SQL inline, which is
        # the safe shape whatever the surrounding arguments happen to be named.
        # Only a call with no literal at all can be passing a built statement.
        #
        # The first version assumed the context argument is always called ctx
        # and flagged `db.ExecContext(c, ` + "`UPDATE ...`" + `, id)` — a
        # perfectly safe inline statement whose context was named c. A linter
        # that cries wolf on correct code gets switched off, so the rule now
        # keys on the presence of a literal rather than on argument names.
        m = CALL.search(line)
        if m and '`' not in line and '"' not in line:
            args = [a.strip() for a in line[m.end():].split(',')]
            for a in args:
                if re.fullmatch(r'[a-zA-Z_]\w*', a) and a not in consts \
                   and a not in ('ctx', 'c', 'err', 'nil', 'tx', 'db'):
                    out.append((path, i + 1,
                                'possible statement passed as a variable: %s' % a, line.strip()))
                    break
    return out


def main():
    root = Path('.')
    show_all = '--list' in sys.argv
    findings, files = [], 0
    for p in sorted(root.rglob('*.go')):
        if any(part in SKIP_DIRS for part in p.parts):
            continue
        files += 1
        findings += scan(p)

    if show_all:
        for p, ln, why, text in findings:
            print('%s:%d  %s' % (p, ln, why))
        return 0

    if not findings:
        print('sqllint: %d files, no SQL built from anything but constants' % files)
        return 0
    print('sqllint: %d finding(s)\n' % len(findings))
    for p, ln, why, text in findings:
        print('  %s:%d' % (p, ln))
        print('      %s' % why)
        print('      %s' % text[:100])
        print('      fix: pass it as a parameter ($1), or make the fragment a const.')
        print('      if it is genuinely safe: // sqllint:allow <reason>')
        print()
    return 1


if __name__ == '__main__':
    sys.exit(main())

# Review: the two-counter parser fix, measured before it exists

Second review of [SUBJECT-PARSING.md](SUBJECT-PARSING.md) §7, requested before
any code changes. The change under review was prose; this review made it
concrete in an isolated harness, established ground truth from the live index,
ran the candidate over every distinct subject in staging, and fanned five
independent audit dimensions plus adversarial verification over the result.

Reviewed on **8 Aug 2026** against a moving staging table (~7.0M distinct
(group, subject) pairs, ~7.6–8.0M article rows across the session — the crawler
runs, counts move; every claim below names its own snapshot).

## Verdict

**The rule is right, the prose was wrong twice, and the change must not ship
alone.**

1. The §7 rule — *counters on both sides of `yEnc`: after is the segment
   counter, before is the file counter* — is confirmed with **zero
   counter-examples** in the entire index, by three independent methods.
2. §7's claim that the same change fixes shape D is **false as written**, and —
   worse — the doc's §2 headline example is **misattributed**: all 8,802
   Superboys articles carry **no `yEnc` at all**. The fix the user is about to
   download-test against would not have touched the Superboys releases. The
   rule must be composed with a no-`yEnc` variant (first counter = file, last =
   segment, when ≥2 counters are present) to cover them; that variant is
   validated on its own population.
3. Shipping the parser change without a one-time staging DELETE leaves 6–30
   hours of junk-building from already-staged wrong rows, plus a narrow
   corrupt-NZB emission path. The DELETE is written and validated read-only.

## Ground truth: which counter is which

Method 1 — **per-post totals variance** (a file counter's total is one constant
per post; segment totals vary per file, rar vs par2):

| population | posts | articles | before-total constant | after-total varies | inverted evidence |
|---|---:|---:|---:|---:|---:|
| yEnc two-counter, ≥20 arts | 124 | 70,707 | **124** | 37 | **0** |
| yEnc two-counter, <20 arts | 164 | ~707 | 164 | — | **0** |
| no-yEnc two-counter, ≥20 arts | 2 | 9,403 | **2** | 1 | **0** |

Method 2 — **volume-in-filename offsets** (`.part004.rar` names its own volume;
the counter that tracks it with a fixed per-post offset is the file counter):
37 yEnc posts with `.partNNN` filenames, 56,937 articles — the before-counter
tracks the volume in **37/37**; the after-counter in **0/37**. Same result for
both no-yEnc posts.

Method 3 — **the adversarial hunt** (independent agent, different queries):
all 1,252 file-groups in the shared-base class map 1:1 to quoted filenames.
Seven candidate misfire shapes were each hunted in the live table and every one
is an **empty population**: title fractions before yEnc, counters inside quoted
filenames, multiple counters after yEnc, `[i/j]` coexisting with parens both
sides, bare `NN/NN` interplay, `(n/1)`/`(1/1)` before-counters, zero
denominators. 1,495 subjects contain `yenc` twice; all are bracketed and never
reach the new branch.

The shape the current code's comment defends — segment counter before `yEnc`,
file-count indicator after — **does not exist in this index**. Its only
attestation anywhere is the synthetic fixture at `subject_test.go:58`.

## The doc's misattribution, corrected

`SUBJECT-PARSING.md` §2 presents the four Superboys releases as Bug A's
product. Wrong:

```
[Superboys.of.Malegaon.2025] (06/23) - "Superboys...WADU.part04.rar" (0683/1621)
```

No `yEnc`. The current parser reads the *segment* counter correctly here (it
takes the last `(a/b)`); what it lacks is the file counter, so all 23 volumes
collide per segment index — which is exactly the observed 1-file/1,621-segment
overlapping fragments. Bug A (BB520) produces a different signature. The doc's
§2 "what it produces" table belongs to shape D, and §7's "A and D share the
fix" is backwards: the yEnc-anchored rule alone fixes A and leaves the
headline damage in place.

Also corrected here: shape A's true size. The doc counted 32,777 articles; the
two-counter population is **71,414** (snapshot of 7.58M rows) — the doc's
regex missed the leading-counter forms (`(002/199) - - "file.part001.rar" -
8,82 GB - yEnc (1/79)`), which are the same bug through a wider door.

## The candidates, run over everything

Two candidates, both in the scratchpad harness against a **verbatim copy** of
`subject.go` (diff-verified, only the package line differs; drift vs the stored
parse of 6,989,483 rows: **zero** bases, **zero** counters):

- **c1** — §7 as written: both sides of `yEnc` → swap.
- **c2** — c1 **plus**: no `yEnc`, ≥2 counters → first = file, last = segment.

| | c1 | c2 |
|---|---:|---:|
| subjects changed | 73,182 | 82,585 |
| intended swap / file-counter-added | 73,182 (all) | 82,585 (all) |
| base changed (must be 0) | **0** | **0** |
| displaced articles in changed population | 71,777 → **0** | 79,673 → **0** |
| collisions introduced anywhere | **0** | **0** |
| shape-D subjects touched | 0 of 115,075 | 9,402 (the Superboys family, on purpose) |
| posts claiming impossible file counts | 0 | 0 |

(The 416 "segment-only" and 4 "other" c1 diffs were chased down individually:
articles where the file and segment numerators coincide numerically —
`(03/80) … yEnc (3/41)` — classified differently by the harness, parsed
correctly by the rule. Nothing unexplained remains.)

**Recommendation: ship c2.** c1 heals 71.4k articles but leaves the exact
releases this investigation started from. c2's extra rule changes only the
9,402-article no-yEnc two-counter family, validated by the same three methods,
and introduces zero collisions.

## Implementation requirements (each one load-bearing)

1. **The `[i/j]` base route must not run for the new branch.**
   `subject.go:190` derives the base from `subject[:fileLoc[0]]` when
   `fileParts` is true — and `fileLoc` is **nil** by construction on the new
   path. A naive "set fileParts=true" panics the crawler; routing through the
   bracket branch instead would produce bases like `"BB520.part001.rar"`
   (quotes, volume, extension intact) or truncate TOWN-banner subjects to the
   banner. The new path keeps `cleanBase(stripAllMarkers(subject))` — verified
   byte-identical to today's staged `base_subject` for every affected row,
   which is what keeps junk memoisation, redis `groupHashKey`, and historical
   grouping stable.
2. **Guard `totalFiles > 0`** (mirror of line 150). The verify pass showed the
   downstream danger is smaller than first claimed — staging's `anyFP` gate
   already requires `TotalFiles > 0` — so this is convention-consistency, not
   a crash guard. Keep it anyway; it is one comparison and it keeps
   `fileParts` meaning what line 150 says it means.
3. **The swap is positional, not value-based.** Four real subjects have
   numerically identical counters on both sides; three more have coinciding
   numerators. An implementation that "detects two distinct counters" by value
   passes everything else and fails these.
4. **`segTotal` keeps meaning per-file total** — it already flows into
   `segFieldKey(fn)` / `st:N` per-file totals; nothing downstream assumes
   `fileParts` implies a bracket in the subject text (audited: every consumer
   of FileNum/TotalFiles/FileParts/SegTotal across crawl, both stagings,
   assemble, salvage, census, telemetry, newznab).

## Deploy transition — the fix must not ship alone

pg staging inserts with `ON CONFLICT (message_id) DO NOTHING`
(`crawl.go:1134`), so **wrong rows already staged are never corrected by
re-crawling** — and the prune horizon is 6h while the prune *job* runs daily
(`prune_interval_min` 1440), so wrong rows outlive the horizon (oldest
observed: 8.2h). Measured live: 73,304 wrong rows across 291 sets, growing
every minute until deploy.

What a do-nothing deploy actually costs — this went through an adversarial
round that overturned the first model: the first new-parse row of a mid-crawl
set flips `bool_or(file_parts)` and permanently disqualifies the mixed set
from BOTH completeness arms, so there is **no ongoing junk stream**; instead
all ~300 in-flight posts are **silently lost** (mixed sets expire unlogged at
the horizon), the final pre-deploy crawl cycle (~15 min) can still junk-build,
a same-base **repost** against a fully-crawled old set can emit a corrupt
N+1-file NZB whose extra file is one arbitrary segment per volume, and a
**rolling deploy** with old crawler workers still staging widens the junk race
— stop old workers with the deploy. The DELETE below eliminates all of it and
is what makes recovery of those posts possible at all.

**Deploy steps:**

1. Deploy the new parser.
2. Run once (idempotent; recount first if run later — both arms validated
   read-only):

```sql
-- arm 1: yEnc two-counter (bug A), 73,304 rows / 291 sets at measurement
DELETE FROM usenet.articles
WHERE file_parts = false
  AND substring(subject from '(?i)^(.*?)\yyenc\y') ~ '\(\d+/\d+\)'
  AND substring(subject from '(?i)\yyenc\y(.*)$') ~ '\(\d+/\d+\)';
-- arm 2 (c2 only): no-yEnc ≥2-counter (Superboys family)
-- validated read-only: 9,403 rows / 2 sets / 0 stranded siblings
DELETE FROM usenet.articles
WHERE file_parts = false
  AND subject !~* '\yyenc\y'
  AND subject ~ '\(\d+/\d+\).*\(\d+/\d+\)';
```

3. Optional recovery of the deleted in-flight posts: admin **group-reset** on
   `alt.binaries.mom`, `.ath`, `.boneless` (98% of affected rows). Only
   meaningful *after* step 2 — a watermark reset alone is a no-op trap, every
   re-read article hits the message_id conflict.
4. **Existing junk releases need manual deletion and are compounding**: the
   four documented Superboys fragments are now **six** (62928 and 63320 landed
   since the doc was written), plus a second family (The.Diplomat.2025 × 5).
   Clean titles and GB sizes defeat every automatic sweep; content_hash cannot
   collapse them; a correct rebuild inserts *alongside* them.
5. Redis-mode deployments (not this site) need their own step: the art:/grp:
   hash TTL refreshes on every touch while a post is mid-crawl, and metaMax
   only raises — poisoned `0:k` fields and `st:0` claims never self-correct.
   DEL the affected sets' keys or flush staging.

## What the fix does NOT do (so nobody expects it)

- **~91% of changed articles become assemblable, not 100%**: 65 shared-base
  posts carry 91% of the articles; the other ~189 "posts" are par2 recovery
  volumes whose bases already splinter per file (`.vol000+01 - 8,82 GB` —
  trailing size text defeats the end-anchored `reVolSuffix`). Their collisions
  stop, but they still expire unbuilt, exactly as today.
- **The one-counter-no-yEnc rar form stays broken** (`"Adobe_Acrobat...part3.rar"
  [RELEASE] (5/20)` — no file counter exists to read; the pinned Ratatouille
  tests cover this and must NOT be rewritten). Fixing it means deriving file
  identity from `.partNNN` in the filename — a different, riskier change, with
  the doc's own measured reasons for caution.
- **Bug B (`payload`) is untouched**, as §7 already said.
- **56 of 291 two-counter bases would index under garbage titles** («3hehrnk86mlv
  - 8,82 GB», banner-only ZED titles, "Description - " audiobook prefixes) —
  no junk rule fires on any of the 291. Deciding whether these should index at
  all is a junk-rules question to take up separately; ~200 of the 291 are
  currently caught later by `allRecoveryVolumes`, 35 by size catchalls.
- **Steady-state cost note**: the pg pre-filter admits a multi-file set once
  every file number is seen; `isComplete` then demands every segment. For a
  225-file post missing one segment that means ~88k rows re-loaded per build
  round until prune — the already-documented admit/refuse gap, ~100× bigger at
  this shape's scale. Accepted (the alternative was building junk), worth a
  cheaper pre-filter later.

## Test plan

Exactly **two** tests fail under the fix, both deliberate tripwires:
`TestTwoCounterSubjectReadsTheFileCounterAsSegments`
(`subject_rarsplit_test.go:115`) and the `"marker before yEnc wins over a
trailing file count"` row (`subject_test.go:57`) — the latter pins the
inverted precedence on a shape with zero attested instances. Rewrites and six
new fixture families are enumerated (leading-counter, TOWN mid-subject banner
— the strongest wrong-base discriminator, realmom `.rN`, vol+par2 (pinning
TODAY'S per-volume base on purpose), the four value-coincidence subjects, and
a per-file `st:N` flow test through the existing completeness harnesses). The
Ratatouille/par pins stay green and stay as they are; their comments (and the
parser's own comment, quoted in them) need the scope correction above.

## How this was verified

Three layers, because the last parser change shipped two regressions and this
one touches every multi-volume post:

1. **Ground truth before any code** — three independent statistical methods
   over the live table, then re-derived a fourth way by a separate agent.
2. **The harness** — a verbatim parser copy (diff-verified) plus both
   candidates, run over all 6,989,483 distinct (group, subject) pairs, with
   every diff classified until nothing unexplained remained, and a second full
   pass hunting collisions the fix would introduce (there are none).
3. **Adversarial review** — five independent audit dimensions (downstream
   consumers, junk/titles/existing damage, deploy transition, misfire shapes,
   test surface), then every actionable finding handed to a separate verifier
   instructed to refute it: **16 of 18 confirmed, 2 overturned**. The core
   rule survived every attack; the two overturned findings were the `(n/0)`
   permanent-stall mechanism (staging re-derives `file_parts` with its own
   guard) and the "ongoing junk stream during a do-nothing window" (mixing
   actually *blocks* junk-building; the real cost is silent loss). The
   corrected versions are what this document states.

Harness, SQL, and per-claim transcripts live in the session scratchpad
(`parsereview/`, `groundtruth.sql`, `enumerate.sql`, `offsets.sql`,
`noyenc.sql`, `tail.sql`) and the workflow journal.

## Appendix: the validated candidate

Exactly what the harness measured (standalone form; the real change lands
inside `parseSubject` with the same three edits — the two-counter branch, the
no-yEnc branch, and the base-route guard):

```go
// inside parseSubject, replacing the segScope block:
fromParens := false
segScope := subject
if !fileParts {
    if loc := reYenc.FindStringIndex(subject); loc != nil {
        if rePartOf.MatchString(subject[:loc[0]]) {
            if after := subject[loc[1]:]; rePartOf.MatchString(after) {
                // Counters on BOTH sides of yEnc: the one before is the file
                // counter, the one after the segment counter. Measured, not
                // assumed: 124 posts / 71k articles, zero counter-examples.
                before := rePartOf.FindAllStringSubmatch(subject[:loc[0]], -1)
                last := before[len(before)-1]
                fileNum = atoi(last[1])
                totalFiles = atoi(last[2])
                fileParts = totalFiles > 0
                fromParens = fileParts
                segScope = after
            } else {
                segScope = subject[:loc[0]]
            }
        }
    } else if counters := rePartOf.FindAllStringSubmatch(subject, -1); len(counters) >= 2 {
        // No yEnc, two or more counters: first = file, last = segment.
        // The Superboys shape. Same validation, its own population.
        fileNum = atoi(counters[0][1])
        totalFiles = atoi(counters[0][2])
        fileParts = totalFiles > 0
        fromParens = fileParts
    }
}
// ... and the base branch becomes:
if fileParts && !fromParens {
    // existing [i/j] release-name derivation, unchanged (fileLoc is non-nil here)
} else {
    base = cleanBase(stripAllMarkers(subject))
}
```

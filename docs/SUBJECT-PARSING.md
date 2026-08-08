# Subject parsing: what the real index says

Measured against the recovered database on **8 Aug 2026**: ~10.06M staged
articles in `usenet.articles`, 56k+ releases in `usenet.nzbs`, crawled from a
live server over several days.

Everything here is from production rows. The earlier analysis in
`docs/BACKLOG.md` #2 was written from a paragraph, and it turned out to be
**wrong about the mechanism** — worth stating plainly, because the wrong version
is what a reader would otherwise carry forward.

> **Nothing is fixed yet.** This is the evidence and the case. The fix changes
> staging for every multi-volume post on a live indexer, and the last change to
> this parser shipped two regressions, so it should be a deliberate decision
> made against these numbers.

> **Reviewed 8 Aug — two corrections, both mine.** (1) §2's headline example
> was misattributed: the Superboys articles carry **no `yEnc` at all** — they
> are shape **D**, and §7's fix as originally written would not have touched
> them. (2) Shape A is **71,414** articles, not 32,777 — the first regex
> missed the leading-counter forms. The corrected fix, its full validation
> (zero counter-examples, zero introduced collisions over 7M subjects), and
> the deploy steps it must ship with are in
> [SUBJECT-PARSING-REVIEW.md](SUBJECT-PARSING-REVIEW.md).

---

## 1. The headline

| | articles | real loss | rate |
|---|---:|---:|---|
| **A** two counters, file before `yEnc` | 32,777 | 32,150 | **98.1%** |
| **B** bracketed `[i/j]` | 8,909,089 | 1,862,955 | 20.9% |
| **C** `yEnc` counter only *(the normal form)* | 1,580,654 | 36,869 | 2.3% |
| **D** a counter, no `yEnc` marker | 13,101 | 10,617 | **81.0%** |
| **E** no counter at all | 38,542 | 1 | 0.0% |

**"Real loss" is not the same as "overwritten."** 24.6% of the whole staging
table shares a key with something else, but most of that is legitimate: the same
subject posted twice, which the staging key correctly dedupes. The column above
counts only keys where **different subjects** collide — one article genuinely
displacing another.

Separating those two was the single most important step in this analysis. Taken
raw, the bracketed form looked 28% broken; it is not.

---

## 2. Bug A — the file counter read as the segment counter

The one that produced the junk releases nobody could explain.

```
"BB520.part001.rar" - (001/225) - yEnc (100/391)
                      ^^^^^^^^^         ^^^^^^^^
                      file 1 of 225     segment 100 of 391
```

`parseSubject` prefers the counter **before** `yEnc`, on its own documented
reasoning:

> *"Prefer the one before yEnc, because for the first form anything after the
> keyword is a file-count indicator rather than a segment counter."*

For this shape that is exactly inverted. The counter before `yEnc` **is** the
file counter, and the real segment counter follows it. So:

| | parsed | should be |
|---|---|---|
| segment | 1 of 225 | 100 of 391 |
| file | 0 of 0 | 1 of 225 |

Every one of the 391 segments of volume 001 becomes "part 1 of 225" and stages
under `formatFieldKey(0, 1)` = `"0:1"`. **291 articles land on that one key.**

Across the index: **32,777 articles in this shape, 103 posts, collapsing onto
563 keys.**

### What it produces

**Correction (8 Aug): this table is real damage but the wrong bug.** Every
Superboys article is `[Title] (06/23) - "file.part04.rar" (0683/1621)` — no
`yEnc` — so these releases are shape **D** (§4), not shape A. Shape A's own
product is the same *kind* of wreck (volumes colliding onto interleaved
fragments); the concrete exhibits below just come from D. Kept here because
the download-test list in §6 is built from them.

`[Superboys.of.Malegaon.2025]` became **four separate releases**, each claiming
a different size, each containing 1 file and 1,621 segments:

| id | claimed size | files | segments |
|---|---|---|---|
| 56262 | 10 GB | 1 | 1621 |
| 59628 | 7917 MB | 1 | 1621 |
| 61867 | 6762 MB | 1 | 1621 |
| 62870 | 7292 MB | 1 | 1621 |

They are **not duplicates** — they overlap partially:

```
56262 vs 61867 : 157 shared segments
56262 vs 62870 : 151 shared segments
59628 vs 61867 : 105 shared segments
59628 vs 62870 : 179 shared segments
```

Interleaved fragments of one post, each presented as a complete release. All
four should fail to assemble.

---

## 3. Bug B — 1.7M posts sharing one identity

The largest collision source by far, and a **different bug** from A.

```
[1/7] - "payload" yEnc (840/1925) 1379557552
[1/9] - "payload" yEnc (3836/6653) 4768729808
```

The filename is literally `payload`. `parseSubject` reduces the subject to that,
so **1,707,570 distinct subjects share `base_subject = 'payload'`** — 1.7M
articles, 17% of the whole staging table, in one bucket per group.

Splitting the bracketed form by base length shows how completely this dominates:

| | real loss |
|---|---:|
| base ≤ 8 chars (`payload`, `r`, `N:/NZB`, `lope.in`) | 1,861,203 |
| base > 8 chars (real names) | **1,752** |

So the bracketed path itself is **healthy**. Its apparent 20.9% loss is almost
entirely obfuscated posts.

These are the posts `whichJunkRule` is meant to drop — the crawler comments call
them *"obfuscated random-token post — never index it"* — and `payload` is not on
the list. Note the trailing number (`1379557552`) is stable per post and differs
between posts, so these **are** distinguishable; the parser discards what would
tell them apart.

**This is arguably not a parsing fix at all.** Either junk them, or key them on
something that survives — but not both halves of the current behaviour, which is
to stage them and let them destroy each other.

---

## 4. Bug D — a counter with no `yEnc` marker

13,101 articles, **81% real loss** — and (corrected 8 Aug) **this is the bug
that made the Superboys releases**, not A. Two sub-shapes:

* **Two counters, no `yEnc`** (~9.4k articles): `[Title] (06/23) -
  "file.part04.rar" (0683/1621)`. The segment counter parses correctly (the
  parser takes the last); the file counter goes unread, so every volume
  collides per segment index. Fixable by the same idea as A with a different
  anchor: **first counter = file, last = segment, when ≥2 are present**.
* **One counter, no `yEnc`** (~3k articles): `"Adobe_...part3.rar" [RELEASE]
  (5/20)`. There is no file counter to read; fixing this means deriving file
  identity from `.partNNN`, a different and riskier change. Stays broken.

---

## 5. What is NOT broken

Worth as much as the bugs, because a fix must not disturb any of it:

* **The normal form (C) is fine** — 2.3% loss across 1.58M articles, and much of
  that will be genuine reposts of a repost.
* **Reposts are deduped correctly.** 61,564 of 61,894 colliding bracketed keys
  are the same subject with a different message-id.
* **Base subjects are derived correctly**, including for the broken shapes.
  `"BB520.part001.rar"` → `BB520` is right; only the counters are wrong.
* **The no-counter path (E) loses nothing** — 1 article out of 38,542.

---

## 6. Releases to download and check

Both broken sets and a control. The expectation is stated so a result either
confirms or refutes it.

### Expected BROKEN — bug A

```
/nzb/56262    [Superboys.of.Malegaon.2025]   claims 10 GB
/nzb/59628    same title                     claims 7917 MB
/nzb/61867    same title                     claims 6762 MB
/nzb/62870    same title                     claims 7292 MB
```

Expect: each downloads as valid NZB XML, and each **fails to assemble** — the
segment set is an overlapping fragment of one post, not a whole file. If any one
of them completes and unpacks, this analysis is wrong and I want to know.

### Expected HEALTHY — control

```
/nzb/78767    Che.dottoressa.ragazzi.1976.1080p.AMZN.WEB-DL     1191 MB, 1 file, 1687 segments
/nzb/69619    Cheer.Up.Hindi.S01E13                              931 MB, 3 files, 1330 segments
```

Expect: these download and assemble normally. If a control fails too, the
problem is downstream of subject parsing and none of the above is the cause.

---

## 7. The fix, and what it risks

**A:** when a subject carries a counter both before and after `yEnc`, the
one **after** is the segment counter and the one **before** is the file counter.
That is a narrowing of the existing rule, not a reversal — the current preference
stays correct for subjects with only one counter.

**D (two-counter half, corrected 8 Aug):** the `yEnc`-anchored rule above does
NOT reach D — D has no `yEnc`. The composed rule that covers both, its
validation, and the staging DELETE it must ship with are in
[SUBJECT-PARSING-REVIEW.md](SUBJECT-PARSING-REVIEW.md).

**B:** a separate decision, and not a parser change. Either add `payload` and
friends to the junk rules, or key obfuscated posts on the trailing token.

Risks, stated because the last change here caused two regressions:

* Every multi-volume post on a live indexer changes staging.
* Existing junk releases do not heal themselves; they need re-crawling or
  deleting.
* 56k releases already built from the current rules stay as they are.

What is different this time: there are ~10M real articles to validate against.
A candidate fix can be run over every distinct subject shape in the index and
checked for **both** properties — that the 32,777 stop colliding, and that
nothing currently parsed correctly changes.

---

## 8. Caveats

* The crawler is running, so counts move. Everything here is one snapshot; the
  staging table grew from 7.2M to 10.06M during the analysis.
* `total_segments` in `usenet.nzbs` is a health-check column and is 0 for 70,958
  of 71,685 rows. It is **not** a segment count — an early version of this
  analysis misread it that way.
* Loss percentages are per-shape, not per-release. A release can survive a
  colliding key if the lost articles were par2.

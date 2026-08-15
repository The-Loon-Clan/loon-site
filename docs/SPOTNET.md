# Spotnet: what it is, and what importing it would take

Findings from a live spike against `free.pt` on 15 Aug 2026. Everything below
that is stated as fact was **read off the wire**, not off documentation — the
published descriptions of the protocol are accurate in outline and useless for
writing a parser.

## What it is

A decentralised index that lives *on* Usenet instead of beside it. A human
"spots" an upload and posts a signed description of it to a public group; the
index is the newsgroup, and every client builds its own copy by reading it.

| group | carries |
| --- | --- |
| `free.pt` | the spots |
| `free.usenet` | comments |
| `alt.binaries.ftd` | the NZB and image each spot points at |

The provider this host is configured against carries `free.pt` with **4,423,231
articles** (7875..4431106) and it is live — the newest spot during the spike was
nine minutes old.

## A spot on the wire

Two independent copies of the metadata: a compact one in the headers that
`XOVER` returns, and the full document in a header it does not.

### The cheap copy, from XOVER

```
Subject: Back To The US Of A (5CD) (2007)
From:    Paaldanser <KEY@27a02b00c08d13z00.3365188124.20.1786812549.1.NL.SIG>
                      │    ││  └ subcats ┘ └ size ───┘ ?? └ posted ─┘ ? └locale
                      │    │└ key id 7 (matches <Key> in the XML)
                      │    └ category 2
                      └ 132-char public-key blob      64-char signature ┘
Message-Id: <p8j99p32S08gpiAagMzxk@spot.net>
```

Size checks out against the titles — 3.36 GB for a 5CD album, 30.6 GB for a
game, 7.14 GB for a 1080p WEB-DL — so a listing can be built from `XOVER`
alone, at roughly one round trip per thousand spots. That is what Spotnet
clients do by default and why they feel fast.

Two fields are still unidentified: the `20` after the size, and the `1` after
the timestamp.

### The full copy, from HEAD

```
X-Xml:            <Spotnet><Posting><Key>7</Key>…   ← ~900 bytes
X-Xml:            …continues…                        ← ~900 bytes
X-Xml:            …continues…                        ← × 6 for this spot
X-Xml-Signature:  OhhseXnixDjsoNIQfWiXmiFCJCdaWVCqoZ5pJ29ytAbGuzlJfMHxApXhJ4F5Kjb5
X-User-Key:       <RSAKeyValue><Modulus>tZ/DNmKoDIXH4P0v9zbalNL2U/ZKYFmkLWMqr1y6dgdkYVoKROkTp18gmu6cMWsl</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>
```

**`X-Xml` is repeated, not folded.** The document is chopped into ~900-byte
pieces, each given its own header line with the same name, and the spot is
their concatenation in order. This is the single most important thing to get
right: a parser that takes the first `X-Xml`, or treats repeats as
alternatives, gets a truncated document that still parses far enough to look
like it worked.

Joined, with the description elided:

```xml
<Spotnet><Posting>
  <Key>7</Key>
  <Created>1786812549</Created>
  <Poster>Paaldanser</Poster>
  <Title>Back To The US Of A (5CD) (2007)</Title>
  <Description>…4534 bytes, [br] for newlines…</Description>
  <Image Width='600' Height='513'><Segment>9n5hBCFmFwggpiAagRPYa@spot.net</Segment></Image>
  <Size>3365188124</Size>
  <Category>02<Sub>02a02</Sub><Sub>02b00</Sub><Sub>02c08</Sub><Sub>02d13</Sub><Sub>02z00</Sub></Category>
  <NZB><Segment>f8xvTXWFhW8hJiAagl8V8@spot.net</Segment></NZB>
  <PREVSPOTS></PREVSPOTS>
</Posting></Spotnet>
```

The public key travels **with the spot**. There is no key directory to fetch:
anyone can post, and the trust model is that clients keep a blacklist of keys
known to spam plus a reputation derived from reports posted to a third group.

Categories are numeric with letter-prefixed sub-lists. Observed: `1` video,
`2` audio, `3` game — consistent across the sample and against the titles. The
canonical table is in Spotweb's source and should be copied from there rather
than inferred.

### The NZB resolves

`<NZB><Segment>` is a bare message-id in `alt.binaries.ftd`. Fetching it:

```
STAT <f8xvTXWFhW8hJiAagl8V8@spot.net> -> 223
BODY                                  -> 266 lines, 242,126 bytes
```

Binary, not yEnc — a compressed payload that inflates to the NZB.

## Why this fits here better than it looks

`usenet.nzbs` is the release table, and it already has the shape:

```
title · size · group_name · content_hash · posted_at
category_id · nzb_data (bytea) · health_status · total_segments
```

`nzb_data` is stored inline. **A spot arrives with a finished NZB**, so an
importer skips the expensive half of the existing pipeline entirely — no
`usenet.articles`, no set resolution, no build outcomes. The crawler's real work
is collecting headers, grouping them into sets and *building* an NZB; Spotnet
hands one over.

```
XOVER free.pt → parse header → HEAD → join X-Xml → verify signature
              → fetch NZB article → inflate → one usenet.nzbs row
```

`loon/nntp` already has `Overview`, `OverviewWithStats` and a connection pool,
so the transport is done.

## What is still unknown

1. **The signature canonicalisation.** The key and signature are both in hand
   and the maths is `crypto/rsa`, but *which bytes* are signed and under which
   digest has to come from Spotweb's verifier. This is the one place where
   getting it wrong is dangerous rather than merely broken: a verifier that
   accepts everything is indistinguishable from one that works, and it is the
   only thing standing between the index and anyone who can post to a public
   group.
2. **The NZB payload encoding** — deflate, and whether anything wraps it.
3. **The two unidentified header fields** (`20`, `1`).
4. **The canonical category table.**

## Recommendation

A `loon-plugins/spotnet` plugin rather than a change to `usenet` — a different
*source* feeding a release model that is already source-neutral, the same
argument as the tracker work in `docs/BACKLOG.md`.

Two things want deciding before it is written:

- **Provenance.** `nzbs.source` is the *media* source (WEB-DL, BluRay), not
  where a row came from. Spotnet rows need their own marker so browse can tell
  a spotted release from a crawled one, and so an operator can retire the feed
  without orphaning rows.
- **Moderation.** The existing junk machinery is regex-over-titles. Spotnet's
  is key-based plus reports. Those want joining, or the structural-junk problem
  arrives again from a new direction — see BACKLOG item 2.

The spike lives in the session scratchpad (`spotprobe/`), not in this
repository: it is 200 lines of stdlib that answered its questions, and its
findings are this document.

# Metadata sources: what exists, what it costs, what is wired

"There are sites like IMDb — is there something like that for books, audio,
software?" Yes for all three, and the differences that matter are **whether you
need an account** and **whether they ship cover art**.

Researched and recorded 8 Aug 2026. The scraper's source registry is in
`main.go`; a source is idle until its credential is set, so an unconfigured
source is silent rather than broken.

## The landscape

| domain | site | key needed | images | notes |
|---|---|---|---|---|
| movie / tv | [TMDB](https://www.themoviedb.org) | **yes**, free signup | posters + backdrops | the IMDb-equivalent with an API you may actually use; IMDb itself has no free public API |
| **books** | [Open Library](https://openlibrary.org) | **no** | covers | Internet Archive project; open catalogue, no signup |
| books | [Google Books](https://developers.google.com/books) | optional | thumbnails | higher quota with a key |
| **audio** | [MusicBrainz](https://musicbrainz.org) + [Cover Art Archive](https://coverartarchive.org) | **no** | covers via CAA | requires a real User-Agent; **1 req/sec per IP**, enforced by blocking |
| audio | [Discogs](https://www.discogs.com/developers) | yes | images | strongest for pressings/editions |
| games | [IGDB](https://api-docs.igdb.com/) | **yes** (Twitch OAuth) | covers, screenshots | needs a Twitch client id + secret exchanged for a bearer token |
| games | Steam `appdetails` | no | header images | Steam titles only |
| software | — | — | — | **no IMDb-equivalent exists.** Software is catalogued by vendor, not centrally |
| anime | [AniDB](https://anidb.net) | client name | posters | already wired |
| xxx | ThePornDB | yes | posters | already wired |

The gap worth knowing: **software has no canonical database.** Games do (IGDB),
because games are products with releases; general applications are not
catalogued anywhere comprehensively. A PC/0day release can be given a category
and a size, but not a poster.

## What is wired here

```
TMDB_API_KEY  → movie + tv     (posters/backdrops)   ← get one to enable video art
TPDB_API_KEY  → xxx
ANIDB_CLIENT  → anime
(none)        → books via Open Library                ← always on
```

**Open Library is registered unconditionally** because it needs no credential.
That matters beyond books: every other source is idle on a fresh checkout, so
before this there was no way to exercise the enrichment path at all without
first going and getting a key.

### The TMDB key was never reaching the container

`main.go` has read `TMDB_API_KEY` since the scraper landed. `docker-compose.yml`
forwarded `TPDB_API_KEY` and `ANIDB_CLIENT` and **not** `TMDB_API_KEY`, so the
variable was absent inside the container no matter what the operator set, and
the source registered as unconfigured. Nothing reported it: an unconfigured
source is a legitimate state.

Fixed by listing it. The rule this file exists to state: **every credential
`main.go` reads must appear in the compose env block**, or setting it does
nothing.

To enable video images now: get a free v3 key from themoviedb.org, then

```sh
echo 'TMDB_API_KEY=your-key' >> .env
docker compose up -d --build app
```

Covers land via the scraper's Catalog Match job and are read back per release
(`catalog_web.go`).

## Constraints these APIs impose

Both keyless sources ask for something in return, and both asks are honoured in
the source code rather than left to chance:

* **Open Library** — "Please, do not crawl our cover API." Covers are therefore
  only ever emitted as URLs for the browser to fetch, never pulled server-side.
  Cover lookups **by ISBN** are limited to 100/IP/5min while lookups **by cover
  id** are not, so `search.json`'s `cover_i` is used and the ISBN path is
  avoided entirely.
* **MusicBrainz** (if added) — 1 request/second/IP, enforced by IP blocking, and
  a meaningful `User-Agent` is mandatory. Any implementation needs a rate
  limiter, not just politeness.

## Adding a source

Implement `catalog.MetadataSource` (Domain / TitleIndex / Fetch / Normalize) and
`scraper.Searcher` for an API-query source, then register it in `main.go`.
`sources/openlibrary` is the smallest complete example — and the only one that
runs without a credential, so it is the one to copy.

Two things a new source must get right, both learned the hard way here:

1. **Route the category.** `domainForCategory` in `scraper/match.go` maps
   Newznab categories to domain keys. Books (7xxx) fell through to `""` until
   this work — no domain, so no match, so no cover, and nothing said why.
   Audio (3xxx) and PC (4xxx) still do.
2. **A no-match is not an error.** Return `ok=false, err=nil`. The scraper
   reports errors; "this release is not a known book" is the normal case.

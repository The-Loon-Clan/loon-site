# How other indexers attach metadata

A survey of `C:\GitHub\Indexer\example` — UNIT3D, NNTmux, NexusPHP and the
original newznab — for the *methods* they use to turn a release name into a
film, a show, an album or a game.

This is about technique, not providers. Which APIs exist is already written
down in [METADATA-SOURCES.md](METADATA-SOURCES.md); what is here is how those
sources get *reached*, which is the harder half and the one this project has
mostly not built.

Read Aug 2026, against the trees in that directory.

## The one structural difference: who supplies the identifier

| | who names the thing |
| --- | --- |
| **UNIT3D** (tracker) | the **uploader**, at upload time |
| **NNTmux** (usenet indexer) | the **indexer**, by inference |
| **loon-site** | the indexer, by inference |

UNIT3D barely has a matching problem. `StoreTorrentRequest` requires an
`imdb` id — validated as `required_with:title_exists_on_imdb` — so a human who
knows what they are uploading hands over the identifier, and TMDB is then called
with a key rather than searched. Its entire `app/Services/Tmdb` is a formatter:
`'https://image.tmdb.org/t/p/original'.$array[$type.'_path']`.

That is worth stating plainly because it makes most of UNIT3D's metadata code
irrelevant to us: a Usenet indexer has nobody to ask. The release name is all
there is, and the release name is frequently a lie.

So NNTmux is the comparator that matters. What follows is its approach.

## NNTmux: a lookup CHAIN per domain, joined on one identifier

`MovieService` is the clearest example. The matching path is an ordered chain,
each step cheaper or more reliable than the next:

```
searchLocalDatabase   →  already matched, or matched for another release
searchIMDb            →  scrape
searchOMDbAPI
searchTraktTV
searchTMDB
similarityPercent     →  fuzzy title comparison, to CONFIRM the hit
```

Two things in that are worth taking.

**The chain ends in a similarity check rather than trusting the first hit.**
`similarityPercent(left, right)` compares the release's parsed title against the
candidate's, so a search that returns something is not the same as a search that
returned the right thing. This is the step a naive implementation omits, and its
absence is invisible: the wrong poster is still a poster.

**One identifier is canonical and every provider is queried by it.** The fetch
side is `fetchTMDBProperties($imdbId)`, `fetchIMDBProperties($imdbId)`,
`fetchTraktTVProperties($imdbId)`, `fetchOmdbAPIProperties($imdbId)` — all
keyed on the **IMDB id**. Discovery may go through any provider; enrichment
always goes through one join key.

That is the opposite of this project, which is TMDB-centric. Neither is wrong,
but IMDB-as-join-key has a practical advantage for Usenet specifically: an IMDB
id is what appears in NFOs and in scene release metadata, so it is the id you
are most likely to be *given* rather than have to infer.

## The methods for getting a name worth searching

This is the part with no equivalent here, and it is most of NNTmux's
`app/Services/NameFixing/`. A Usenet subject is often obfuscated, so before any
lookup there is a stage whose whole job is *recovering the real name*. It is
structured as extractors and checkers:

**Extractors — where a candidate name comes from**

| extractor | source |
| --- | --- |
| `FileNameExtractor` | the filenames inside the release |
| `NfoNameExtractor` | the NFO shipped with it |
| `ObfuscatedSubjectExtractor` | the subject itself, when it is scrambled |

**Checkers — deciding whether a candidate is plausible for that media type**

`MovieNameChecker`, `TvNameChecker`, `GameNameChecker`, `AppNameChecker` — one
per domain, because "looks like a film name" and "looks like an application"
are different questions.

**Selectors — choosing between candidates**

`PredbMatchSelector` and `DonorMatchSelector`. The preDB one is the interesting
half: `IRCScraper` sits in scene IRC channels recording release names as they
are announced, into `Predb` / `PredbHash` tables. A hash of the obfuscated
subject can then be matched against a known-good name that was published before
the post appeared.

That is a genuinely different class of technique from anything here: it does not
parse harder, it obtains the answer from outside.

Supporting cast: `Par2Processor` (real filenames out of PAR2 recovery blocks),
`ReleaseCleaningService`, `RegexService` (site-configurable regexes per group),
`FileNameCleaner`, `NzbSplitUnwrapper`.

## Post-processing runs as one pipeline of per-domain processors

`PostProcessService` dispatches to `NfoProcessor`, `MoviesProcessor`,
`TvProcessor`, `MusicProcessor`, `BooksProcessor`, `GamesProcessor`,
`ConsolesProcessor`, `AnimeProcessor`.

`NfoProcessor` runs first, and that ordering is the point: the NFO is where the
identifiers are. Everything downstream is cheaper when it has an id rather than
a title.

Each domain then has its own provider set, and they are not interchangeable:

| domain | providers used |
| --- | --- |
| movies | IMDB, OMDb, Trakt, TMDB, Fanart.tv |
| tv | TVmaze (283 mentions), TheTVDB, Trakt |
| anime | AniDB (594 mentions — the most-integrated source in the tree), AniList, MyAnimeList |
| books | Open Library, Google Books, ISBNdb, iTunes |
| games / console | IGDB, iTunes |
| music | MusicBrainz-shaped lookup by artist + album |

Note the shape of the by-name lookups: `getMusicInfoByName(artist, album)`,
`getConsoleInfoByName(title, platform)`, `getBookInfoByName(title, parsed)`.
Each takes the *fields that disambiguate that domain* rather than a single
string — an album needs its artist, a game needs its platform.

## What NexusPHP does

Nothing comparable in `app/`. Its metadata handling is in the legacy `include/`
and `nexus/` trees, and it is a tracker in UNIT3D's mould — the uploader
supplies the identifier. Not a useful comparator for a Usenet indexer.

## What this suggests for loon-site

Ordered by what would change the most, given the site currently matches on a
parsed title through the scraper plugin's `MetadataSource` registry.

1. **Confirm a match before accepting it.** A similarity check between the
   parsed release title and the candidate's title, as `similarityPercent` does.
   Cheap, and it converts "we found something" into "we found the right thing".
   The failure it prevents is silent and permanent: a wrong poster looks exactly
   like a right one.

2. **Read the NFO.** It is the highest-value unexploited source in a Usenet
   release: the identifiers are frequently sitting in it, and an id beats any
   amount of title parsing. `NfoProcessor` running first is NNTmux's ordering
   and it is the right one.

3. **Pick a canonical join key deliberately.** Discovery through several
   providers, enrichment through one id. This project already populates catalog
   cross-ids, so the machinery is there; what is missing is the decision about
   which id is the spine.

4. **Domain-aware lookups.** `getBookInfoByName(title)` cannot disambiguate a
   book, and `getConsoleInfoByName(title, platform)` shows the fix. Our sources
   take a title; several of the domains need a second field.

5. **preDB.** Highest effort, and the only one that obtains the answer rather
   than deducing it. Worth recording as an option rather than a plan — it needs
   an IRC presence and a table of announcements, and it only helps for scene
   releases.

Not recommended: copying UNIT3D's model. Asking an uploader for the id is the
correct design for a tracker and unavailable to an indexer that crawls.

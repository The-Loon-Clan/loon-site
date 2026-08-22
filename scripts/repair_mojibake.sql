-- One-off repair for docs/BACKLOG.md #9: C1 control characters in release
-- titles. Run once, on 2026-08-22, AFTER the ingest fix in loon-plugins
-- c5c216a. Repairing before that would only re-break on the next crawl.
--
--   docker compose exec -T db psql -U demo -d loon_demo -f - < scripts/repair_mojibake.sql
--
-- THE DAMAGE. An encoded-word declared ISO-8859-1 and carried UTF-8, so every
-- byte was widened to the code point of the same value: "Español" became
-- "EspaÃ±ol", and a right single quote (E2 80 99) became "â" followed by
-- U+0080 U+0099 -- two C1 controls, which are forbidden in HTML.
--
-- THE REVERSAL is the encode run backwards: narrow each rune to one byte, read
-- the bytes as UTF-8. Applied repeatedly, because a few titles were widened
-- twice: "ō" survives one pass as "Å" + U+008D and needs a second.
--
-- IDEMPOTENT. The WHERE clause matches only titles that still carry a C1
-- control, and a repaired title carries none, so a second run changes nothing.
--
-- WHAT IT CANNOT FIX. Ten titles lost the LEAD byte of a two-byte sequence
-- before they were stored -- all ten are Bleach episode names where "ō" is
-- now "o" followed by an orphan U+008D. The character is gone and no reversal
-- invents it back. The orphan byte is stripped, which leaves the ordinary
-- macron-less romanisation (Tosen, Kyoraku, Zanpakuto) and valid HTML. That is
-- a loss, and it is recorded here rather than hidden by the count going to 0.

CREATE OR REPLACE FUNCTION pg_temp.unmoji(t text) RETURNS text AS $f$
DECLARE out text := t; nxt text;
BEGIN
  -- Four passes is a ceiling, not an expectation: two is the deepest seen.
  FOR i IN 1..4 LOOP
    BEGIN
      nxt := convert_from(convert_to(out, 'LATIN1'), 'UTF8');
    EXCEPTION WHEN others THEN
      RETURN out;   -- a rune past a byte, or bytes that are not UTF-8: stop
    END;
    IF nxt = out THEN RETURN out; END IF;
    out := nxt;
  END LOOP;
  RETURN out;
END $f$ LANGUAGE plpgsql IMMUTABLE;

-- fixed() is what every column below is put through: reverse as far as it
-- goes, then drop any C1 that survived (see "WHAT IT CANNOT FIX" above).
CREATE OR REPLACE FUNCTION pg_temp.fixed(t text) RETURNS text AS $g$
  SELECT regexp_replace(pg_temp.unmoji(t),
                        '[' || chr(128) || '-' || chr(159) || ']', '', 'g');
$g$ LANGUAGE sql IMMUTABLE;

-- FOUR COLUMNS, not one. The first scan looked at usenet.nzbs.title alone and
-- reported it clean afterwards, which was true and not the same as finished:
--
--   usenet.nzbs.filename            2,001  the NZB filename, taken from the title
--   usenet.nzbs.series_name           245  what /series GROUPS BY -- mojibake here
--                                          splits one show into two shows
--   usenet.set_resolutions.base_subject  719  diagnostic samples
--   usenet.subject_corpus.subject        138  diagnostic samples
--
-- series_name is the one that was doing visible harm beyond the invalid HTML.

\echo 'before:'
SELECT 'nzbs.title' AS col, count(*) FROM usenet.nzbs WHERE title ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'nzbs.filename', count(*) FROM usenet.nzbs WHERE filename ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'nzbs.series_name', count(*) FROM usenet.nzbs WHERE series_name ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'set_resolutions.base_subject', count(*) FROM usenet.set_resolutions WHERE base_subject ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'subject_corpus.subject', count(*) FROM usenet.subject_corpus WHERE subject ~ ('[' || chr(128) || '-' || chr(159) || ']');

UPDATE usenet.nzbs SET title = pg_temp.fixed(title)
 WHERE title ~ ('[' || chr(128) || '-' || chr(159) || ']');
UPDATE usenet.nzbs SET filename = pg_temp.fixed(filename)
 WHERE filename ~ ('[' || chr(128) || '-' || chr(159) || ']');
UPDATE usenet.nzbs SET series_name = pg_temp.fixed(series_name)
 WHERE series_name ~ ('[' || chr(128) || '-' || chr(159) || ']');
UPDATE usenet.set_resolutions SET base_subject = pg_temp.fixed(base_subject)
 WHERE base_subject ~ ('[' || chr(128) || '-' || chr(159) || ']');
UPDATE usenet.subject_corpus SET subject = pg_temp.fixed(subject)
 WHERE subject ~ ('[' || chr(128) || '-' || chr(159) || ']');

\echo 'after (every one must be 0):'
SELECT 'nzbs.title' AS col, count(*) FROM usenet.nzbs WHERE title ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'nzbs.filename', count(*) FROM usenet.nzbs WHERE filename ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'nzbs.series_name', count(*) FROM usenet.nzbs WHERE series_name ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'set_resolutions.base_subject', count(*) FROM usenet.set_resolutions WHERE base_subject ~ ('[' || chr(128) || '-' || chr(159) || ']')
UNION ALL SELECT 'subject_corpus.subject', count(*) FROM usenet.subject_corpus WHERE subject ~ ('[' || chr(128) || '-' || chr(159) || ']');

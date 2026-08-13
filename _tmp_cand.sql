-- Level 1: dedupe key granularity (file_num, part_num) per set
WITH k AS (
  SELECT group_name, base_subject, file_num, part_num,
         COUNT(*)            AS cnt,
         SUM(bytes)          AS sum_bytes,
         MIN(bytes)          AS min_bytes,
         MAX(bytes)          AS max_bytes,
         MAX(seg_total)      AS max_seg_total,
         MAX(total_parts)    AS max_total_parts,
         MAX(total_files)    AS max_total_files,
         bool_or(file_parts) AS any_fp
  FROM usenet.articles
  GROUP BY group_name, base_subject, file_num, part_num
),
-- Level 2: per file bucket
f AS (
  SELECT group_name, base_subject, file_num,
         SUM(cnt)                 AS arts,
         SUM(sum_bytes)           AS sum_bytes,
         SUM(min_bytes)           AS dedup_min,
         SUM(max_bytes)           AS dedup_max,
         COUNT(*)                 AS distinct_parts,
         MAX(max_seg_total)       AS seg_total,
         MAX(max_total_parts)     AS total_parts,
         MAX(max_total_files)     AS total_files,
         bool_or(any_fp)          AS any_fp
  FROM k GROUP BY group_name, base_subject, file_num
),
-- Level 3: per set
s AS (
  SELECT group_name, base_subject,
         SUM(arts)                                  AS arts,
         SUM(sum_bytes)                             AS sum_bytes,
         SUM(dedup_min)                             AS dedup_min,
         SUM(dedup_max)                             AS dedup_max,
         SUM(distinct_parts)                        AS keys_fp,      -- dedupe by (file,part)
         bool_or(any_fp)                            AS any_fp,       -- buildNZB multi
         MAX(total_files)                           AS max_total_files,
         MAX(total_parts)                           AS max_total_parts,
         COUNT(*) FILTER (WHERE file_num >= 1)      AS distinct_files_ge1,
         -- isComplete multi arm: every seen bucket must have seg_total>0 and parts>=seg_total
         bool_and(seg_total > 0 AND distinct_parts >= seg_total) AS all_buckets_full,
         COUNT(*) FILTER (WHERE file_num >= 1 AND seg_total > 0
                            AND distinct_parts >= seg_total)      AS complete_ge1
  FROM f GROUP BY group_name, base_subject
),
-- single-file dedupe is by part_num ALONE across the whole set (buildNZB !multi)
p AS (
  SELECT group_name, base_subject, COUNT(DISTINCT part_num) AS keys_p
  FROM usenet.articles GROUP BY group_name, base_subject
),
-- isComplete's multi predicate needs file_parts AND total_files>0 on the SAME row
icm AS (
  SELECT group_name, base_subject,
         bool_or(file_parts AND total_files > 0) AS ic_multi,
         MAX(total_files) FILTER (WHERE file_parts AND total_files > 0) AS ic_total_files
  FROM usenet.articles GROUP BY group_name, base_subject
)
SELECT s.group_name, s.base_subject, s.arts, s.sum_bytes, s.dedup_min, s.dedup_max,
       s.keys_fp, p.keys_p, s.any_fp, icm.ic_multi, icm.ic_total_files,
       s.max_total_parts, s.distinct_files_ge1, s.max_total_files,
       s.all_buckets_full, s.complete_ge1
FROM s JOIN p USING (group_name, base_subject) JOIN icm USING (group_name, base_subject)
-- candidateGroups pre-filter, verbatim
WHERE ( (s.any_fp = FALSE AND p.keys_p >= s.max_total_parts)
     OR (s.any_fp = TRUE  AND s.distinct_files_ge1 >= s.max_total_files) );

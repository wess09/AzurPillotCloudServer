-- Keep raw reports durable while exposing each stable event only once.
DROP INDEX stat_events_record_identity;
CREATE INDEX stat_events_record_identity ON stat_events(record_id) WHERE record_id<>'';
CREATE INDEX stat_review_events_record_identity ON stat_review_events(record_id) WHERE record_id<>'';

ALTER TABLE stat_review_items DROP CONSTRAINT stat_review_items_pkey;
ALTER TABLE stat_review_items ADD PRIMARY KEY(sha256,ordinal,item,tag,local_key,candidate_id);
-- Recover identities merged by 0004 from the unchanged original result payload.
INSERT INTO stat_review_items(sha256,ordinal,tag,amount,local_key,candidate_id)
SELECT r.sha256,(e.ordinality-1)::integer,COALESCE(i.value->>'tag',''),
       sum((i.value->>'amount')::bigint),i.value->>'local_key',COALESCE(i.value->>'candidate_id','')
FROM stat_results r
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(r.payload->'events')='array' THEN r.payload->'events' ELSE '[]'::jsonb END) WITH ORDINALITY e(value,ordinality)
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.value->'unresolved_items')='array' THEN e.value->'unresolved_items' ELSE '[]'::jsonb END) i(value)
JOIN stat_review_events v ON v.sha256=r.sha256 AND v.ordinal=e.ordinality-1
WHERE r.status='needs_review'
GROUP BY r.sha256,e.ordinality,COALESCE(i.value->>'tag',''),i.value->>'local_key',COALESCE(i.value->>'candidate_id','')
ON CONFLICT(sha256,ordinal,item,tag,local_key,candidate_id) DO UPDATE SET amount=EXCLUDED.amount;

ALTER TABLE unknown_hashes ADD COLUMN formal_revision BIGINT NOT NULL DEFAULT -1;
CREATE INDEX unknown_hashes_cluster ON unknown_hashes(cluster_key);
ALTER TABLE unknown_clusters ALTER COLUMN representative_sha256 DROP NOT NULL;
ALTER TABLE unknown_clusters DROP CONSTRAINT unknown_clusters_status_check;
ALTER TABLE unknown_clusters ADD CHECK(status IN ('pending','assigned','ignored','merged','empty'));
ALTER TABLE unknown_clusters ADD CHECK(representative_sha256 IS NOT NULL OR status IN ('empty','merged'));

CREATE VIEW unknown_effective_hashes AS
SELECT h.local_key,k.canonical_key,
 CASE WHEN o.local_key IS NOT NULL OR h.cluster_key IS NOT NULL THEN m.item
      WHEN h.formal_revision=COALESCE(a.release_id,0) THEN h.formal_item END AS item,
 (o.local_key IS NOT NULL OR h.cluster_key IS NOT NULL OR h.formal_revision=COALESCE(a.release_id,0)) AS is_current
FROM unknown_hashes h
CROSS JOIN template_active a
LEFT JOIN unknown_hash_overrides o ON o.local_key=h.local_key
LEFT JOIN unknown_canonical_keys k ON k.key=COALESCE(o.cluster_key,h.cluster_key)
LEFT JOIN unknown_mappings m ON m.cluster_key=k.canonical_key AND m.revision=a.release_id
WHERE a.id;

CREATE OR REPLACE VIEW stat_resolved_review_items AS
SELECT i.sha256,i.ordinal,i.tag,i.amount,
 CASE WHEN i.local_key<>'' THEN h.item WHEN i.item LIKE 'U-%' THEN m.item ELSE i.item END AS item
FROM stat_review_items i
LEFT JOIN unknown_effective_hashes h ON h.local_key=i.local_key
LEFT JOIN unknown_canonical_keys k ON k.key=i.item
LEFT JOIN unknown_mappings m ON m.cluster_key=k.canonical_key AND m.revision=(SELECT release_id FROM template_active WHERE id);

CREATE OR REPLACE VIEW stat_resolved_review_groups AS
SELECT r.sha256 FROM stat_results r
WHERE r.status='needs_review'
 AND EXISTS(SELECT 1 FROM stat_review_events e WHERE e.sha256=r.sha256)
 AND EXISTS(SELECT 1 FROM stat_review_items i WHERE i.sha256=r.sha256 AND (i.local_key<>'' OR i.item LIKE 'U-%'))
 AND EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.payload->'diagnostics')='array' THEN r.payload->'diagnostics' ELSE '[]'::jsonb END) d(value)
             WHERE d.value='missing_template' OR d.value LIKE 'missing_template:%' OR d.value LIKE 'unmapped_cluster:%')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.payload->'diagnostics')='array' THEN r.payload->'diagnostics' ELSE '[]'::jsonb END) d(value)
                WHERE d.value<>'missing_template' AND d.value NOT LIKE 'missing_template:%' AND d.value NOT LIKE 'unmapped_cluster:%')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.payload->'candidate_ids')='array' THEN r.payload->'candidate_ids' ELSE '[]'::jsonb END) c(value)
                WHERE NOT EXISTS(SELECT 1 FROM stat_review_items i WHERE i.sha256=r.sha256 AND i.candidate_id=c.value))
 AND NOT EXISTS(SELECT 1 FROM stat_resolved_review_items i WHERE i.sha256=r.sha256 AND i.item IS NULL);

CREATE OR REPLACE VIEW stat_effective_events AS
SELECT e.* FROM stat_events e
WHERE e.record_id='' OR NOT EXISTS(
 SELECT 1 FROM stat_events p WHERE p.record_id<>'' AND p.record_id=e.record_id AND (p.sha256,p.ordinal)<(e.sha256,e.ordinal)
)
UNION ALL
SELECT e.* FROM stat_review_events e JOIN stat_resolved_review_groups g USING(sha256)
WHERE e.record_id='' OR (
 NOT EXISTS(SELECT 1 FROM stat_events p WHERE p.record_id<>'' AND p.record_id=e.record_id)
 AND NOT EXISTS(SELECT 1 FROM stat_review_events p JOIN stat_resolved_review_groups q ON q.sha256=p.sha256
                WHERE p.record_id<>'' AND p.record_id=e.record_id AND (p.sha256,p.ordinal)<(e.sha256,e.ordinal))
);

CREATE OR REPLACE VIEW stat_effective_items AS
-- Consumers join these item rows to stat_effective_events by source/ordinal.
-- Re-expanding the event eligibility view here multiplies the query planner's
-- work and prevents large mixed summaries from staying bounded.
SELECT sha256,ordinal,item,tag,amount FROM stat_items
UNION ALL
SELECT i.sha256,i.ordinal,i.item,i.tag,sum(i.amount)::bigint FROM stat_resolved_review_items i
JOIN stat_resolved_review_groups g USING(sha256)
GROUP BY i.sha256,i.ordinal,i.item,i.tag;

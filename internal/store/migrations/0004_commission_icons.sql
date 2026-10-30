ALTER TABLE stat_events
  ADD COLUMN record_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN comm TEXT,
  ADD COLUMN name TEXT NOT NULL DEFAULT '',
  ADD COLUMN commission_status SMALLINT CHECK (commission_status IN (0,1));
CREATE UNIQUE INDEX stat_events_record_identity ON stat_events(record_id) WHERE record_id<>'';

CREATE TABLE stat_review_events (LIKE stat_events INCLUDING DEFAULTS INCLUDING CONSTRAINTS);
ALTER TABLE stat_review_events ADD PRIMARY KEY (sha256,ordinal);
ALTER TABLE stat_review_events ADD FOREIGN KEY (sha256) REFERENCES stat_results(sha256);
CREATE INDEX stat_review_events_scope ON stat_review_events(occurred_at,server,scene);
CREATE TABLE stat_review_items (
  sha256 TEXT NOT NULL, ordinal INTEGER NOT NULL,
  item TEXT NOT NULL DEFAULT '', tag TEXT NOT NULL DEFAULT '',
  amount BIGINT NOT NULL CHECK(amount>0),
  local_key TEXT NOT NULL DEFAULT '', candidate_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(sha256,ordinal,item,tag,local_key),
  FOREIGN KEY(sha256,ordinal) REFERENCES stat_review_events(sha256,ordinal) ON DELETE CASCADE
);

CREATE TABLE unknown_icons (
  icon_sha256 TEXT PRIMARY KEY CHECK(icon_sha256 ~ '^[0-9a-f]{64}$'),
  image TEXT NOT NULL, phash TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE unknown_clusters (
  id BIGSERIAL PRIMARY KEY,
  key TEXT GENERATED ALWAYS AS ('U-' || id::text) STORED UNIQUE,
  representative_sha256 TEXT NOT NULL REFERENCES unknown_icons(icon_sha256),
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','assigned','ignored','merged')),
  hits BIGINT NOT NULL DEFAULT 0, first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE unknown_hashes (
  local_key TEXT PRIMARY KEY REFERENCES unknown_icons(icon_sha256),
  cluster_key TEXT REFERENCES unknown_clusters(key), formal_item TEXT,
  CHECK((cluster_key IS NULL) <> (formal_item IS NULL))
);
CREATE TABLE unknown_cluster_exemplars (
  cluster_key TEXT NOT NULL REFERENCES unknown_clusters(key),
  icon_sha256 TEXT NOT NULL REFERENCES unknown_icons(icon_sha256),
  PRIMARY KEY(cluster_key,icon_sha256)
);
CREATE TABLE unknown_state (id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(id), generation BIGINT NOT NULL DEFAULT 0);
INSERT INTO unknown_state(id) VALUES(TRUE);
ALTER TABLE template_candidates ADD COLUMN icon_sha256 TEXT REFERENCES unknown_icons(icon_sha256);
CREATE INDEX template_candidates_icon ON template_candidates(icon_sha256);
CREATE TABLE unknown_mapping_drafts (
  cluster_key TEXT PRIMARY KEY REFERENCES unknown_clusters(key), item TEXT NOT NULL
);
CREATE TABLE unknown_mappings (
  revision BIGINT NOT NULL REFERENCES template_releases(id),
  cluster_key TEXT NOT NULL REFERENCES unknown_clusters(key), item TEXT NOT NULL,
  PRIMARY KEY(revision,cluster_key)
);
CREATE TABLE unknown_cluster_aliases (
  from_key TEXT PRIMARY KEY REFERENCES unknown_clusters(key),
  to_key TEXT NOT NULL REFERENCES unknown_clusters(key), revision BIGINT NOT NULL DEFAULT 0,
  CHECK(from_key<>to_key)
);
CREATE TABLE unknown_hash_overrides (
  local_key TEXT PRIMARY KEY REFERENCES unknown_icons(icon_sha256),
  cluster_key TEXT NOT NULL REFERENCES unknown_clusters(key)
);
CREATE TABLE unknown_screenshots (
  cluster_key TEXT NOT NULL REFERENCES unknown_clusters(key),
  sample_sha256 TEXT NOT NULL REFERENCES stat_results(sha256),
  file_index INTEGER NOT NULL CHECK(file_index>=0), frame_index INTEGER NOT NULL CHECK(frame_index BETWEEN 0 AND 9),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL DEFAULT now()+interval '90 days',
  PRIMARY KEY(cluster_key,sample_sha256,file_index,frame_index)
);
ALTER TABLE template_releases
  ADD COLUMN signature TEXT NOT NULL DEFAULT '',
  ADD COLUMN public_key TEXT NOT NULL DEFAULT '';
CREATE TABLE template_publish_settings(id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(id),thresholds JSONB);
INSERT INTO template_publish_settings(id) VALUES(TRUE);

CREATE VIEW unknown_canonical_keys AS
WITH RECURSIVE chain(key,target,depth) AS (
  SELECT key,key,0 FROM unknown_clusters
  UNION ALL
  SELECT chain.key,a.to_key,chain.depth+1 FROM chain JOIN unknown_cluster_aliases a ON a.from_key=chain.target WHERE chain.depth<32
)
SELECT DISTINCT ON(key) key,target AS canonical_key FROM chain ORDER BY key,depth DESC;

CREATE VIEW stat_resolved_review_items AS
SELECT i.sha256,i.ordinal,i.tag,i.amount,
 CASE WHEN i.local_key<>'' THEN CASE WHEN o.local_key IS NOT NULL THEN m.item ELSE COALESCE(h.formal_item,m.item) END
      WHEN i.item LIKE 'U-%' THEN m.item ELSE i.item END AS item
FROM stat_review_items i
LEFT JOIN unknown_hashes h ON h.local_key=i.local_key
LEFT JOIN unknown_hash_overrides o ON o.local_key=i.local_key
LEFT JOIN unknown_canonical_keys k ON k.key=CASE WHEN i.local_key<>'' THEN COALESCE(o.cluster_key,h.cluster_key) ELSE i.item END
LEFT JOIN unknown_mappings m ON m.cluster_key=k.canonical_key AND m.revision=(SELECT release_id FROM template_active WHERE id);

CREATE VIEW stat_resolved_review_groups AS
SELECT r.sha256 FROM stat_results r
WHERE r.status='needs_review'
 AND EXISTS(SELECT 1 FROM stat_review_events e WHERE e.sha256=r.sha256)
 AND EXISTS(SELECT 1 FROM stat_review_items i WHERE i.sha256=r.sha256 AND (i.local_key<>'' OR i.item LIKE 'U-%'))
 AND EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.payload->'diagnostics')='array' THEN r.payload->'diagnostics' ELSE '[]'::jsonb END) AS d(value)
             WHERE d.value LIKE 'missing_template:%' OR d.value LIKE 'unmapped_cluster:%')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.payload->'diagnostics')='array' THEN r.payload->'diagnostics' ELSE '[]'::jsonb END) AS d(value)
                WHERE d.value NOT LIKE 'missing_template:%' AND d.value NOT LIKE 'unmapped_cluster:%')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.payload->'candidate_ids')='array' THEN r.payload->'candidate_ids' ELSE '[]'::jsonb END) AS c(value)
                WHERE NOT EXISTS(SELECT 1 FROM stat_review_items i WHERE i.sha256=r.sha256 AND i.candidate_id=c.value))
 AND NOT EXISTS(SELECT 1 FROM stat_resolved_review_items i WHERE i.sha256=r.sha256 AND i.item IS NULL);

CREATE VIEW stat_effective_events AS
SELECT * FROM stat_events
UNION ALL
SELECT e.* FROM stat_review_events e JOIN stat_resolved_review_groups g USING(sha256);

CREATE VIEW stat_effective_items AS
SELECT sha256,ordinal,item,tag,amount FROM stat_items
UNION ALL
SELECT i.sha256,i.ordinal,i.item,i.tag,sum(i.amount)::bigint FROM stat_resolved_review_items i
JOIN stat_resolved_review_groups g USING(sha256) GROUP BY i.sha256,i.ordinal,i.item,i.tag;

CREATE VIEW stat_effective_results AS
SELECT r.sha256,r.revision,CASE WHEN g.sha256 IS NOT NULL THEN 'complete' ELSE r.status END AS status,r.payload,r.updated_at
FROM stat_results r LEFT JOIN stat_resolved_review_groups g USING(sha256);

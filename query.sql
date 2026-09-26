-- name: UpsertBundle :one
INSERT INTO bundles (object_key) VALUES ($1)
ON CONFLICT (object_key) DO UPDATE SET object_key = EXCLUDED.object_key
RETURNING id;

-- name: DeleteBundleEntries :exec
DELETE FROM bundle_entries WHERE bundle_id = $1;

-- name: InsertBundleEntries :exec
INSERT INTO bundle_entries (bundle_id, frame_offset, url)
SELECT $1, positions.frame_position, urls.url
FROM unnest(sqlc.arg(frame_offsets)::bigint[]) WITH ORDINALITY AS positions(frame_position, n)
JOIN unnest(sqlc.arg(urls)::text[]) WITH ORDINALITY AS urls(url, n) USING (n);

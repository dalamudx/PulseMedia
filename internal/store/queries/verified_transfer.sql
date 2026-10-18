-- name: LockVerifiedTransferStep :one
SELECT * FROM transfer_step WHERE id = $1 FOR UPDATE;

-- name: GetVerifiedTransferContent :one
SELECT content FROM transfer_content_evidence WHERE step_id=$1;

-- name: FreezeVerifiedTransferContent :one
INSERT INTO transfer_content_evidence(step_id,content)
SELECT s.id,sqlc.arg(content)::jsonb FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id
WHERE s.id=sqlc.arg(id) AND s.state='running' AND p.state='executing'
 AND s.action IN ('copy','reflink','move') AND p.strategy=s.action AND p.requires_approval
 AND s.target_evidence->'file_action' IS NOT NULL
ON CONFLICT(step_id) DO NOTHING RETURNING content;

-- name: InheritVerifiedTransferContent :execrows
INSERT INTO transfer_content_evidence(step_id,content,inherited_from)
SELECT n.id,e.content,e.step_id FROM transfer_content_evidence e
JOIN transfer_step o ON o.id=e.step_id
JOIN transfer_step n ON n.id=sqlc.arg(id)
JOIN transfer_plan p ON p.id=n.plan_id
WHERE e.step_id=sqlc.arg(previous_step_id) AND n.state='pending' AND p.state='approved'
 AND n.action IN ('copy','reflink','move') AND p.strategy=n.action AND p.requires_approval
 AND n.target_evidence->'file_action' IS NOT NULL
 AND ROW(n.source_root,n.source_rel,n.source_size,n.source_mtime,n.source_device,n.source_inode,n.unit_id)
     IS NOT DISTINCT FROM ROW(o.source_root,o.source_rel,o.source_size,o.source_mtime,o.source_device,o.source_inode,o.unit_id);

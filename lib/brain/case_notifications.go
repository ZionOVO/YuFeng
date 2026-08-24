package brain

import "context"

func notifyCaseSessions(ctx context.Context, db dbTX, sender, assetID, kind, refID, summary string) error {
	if sender == "" {
		sender = "jarvis-1"
	}
	_, err := db.Exec(ctx, `WITH eligible AS (
		SELECT DISTINCT s.owner
		FROM sessions s JOIN grants g ON g.subject_kind='user' AND g.subject_id=s.owner
		WHERE (g.expires_at IS NULL OR g.expires_at>now())
		  AND g.tools ? 'case.read'
		  AND EXISTS (SELECT 1 FROM jsonb_array_elements(g.bindings) b
			WHERE COALESCE(b->>'kind','asset')='asset' AND b->>'id'=$1)
	), latest AS (
		SELECT DISTINCT ON (s.owner) s.session_id
		FROM sessions s JOIN eligible e ON e.owner=s.owner
		ORDER BY s.owner, s.created_at DESC
	)
	INSERT INTO session_messages(session_id, sender, content, attachments)
	SELECT session_id, $2, $5,
		jsonb_build_array(jsonb_build_object('kind',$3::text,'ref_id',$4::text,'module_id','traffic-interception'))
	FROM latest`, assetID, sender, kind, refID, summary)
	return err
}

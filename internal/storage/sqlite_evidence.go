package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"draw/internal/model"
)

type SQLiteEvidenceStore struct {
	db *sql.DB
}

func NewSQLiteEvidenceStore(db *sql.DB) (*SQLiteEvidenceStore, error) {
	return &SQLiteEvidenceStore{db: db}, nil
}

func (s *SQLiteEvidenceStore) Init(ctx context.Context) error {
	return nil
}

func (s *SQLiteEvidenceStore) Put(e model.Evidence) (model.EvidenceID, error) {
	id := e.ID
	if id == "" {
		id = model.NewEvidenceID()
	}
	ctx := context.Background()
	const q = `INSERT INTO evidence
  (id, session_id, task_id, source_id, topic, claim, value, confidence, verification_state, collected_at, origin_url, extraction_seq)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  claim = excluded.claim,
  value = excluded.value,
  confidence = excluded.confidence,
  verification_state = excluded.verification_state,
  collected_at = excluded.collected_at,
  origin_url = excluded.origin_url,
  extraction_seq = excluded.extraction_seq`
	if _, err := s.db.ExecContext(ctx, q,
		string(id),
		string(e.SessionID),
		string(e.TaskID),
		string(e.SourceID),
		nullString(e.Topic),
		nullString(e.Claim),
		nullString(e.Value),
		e.Confidence,
		string(e.Verification),
		e.CollectedAt,
		ptrString(e.OriginURL),
		ptrInt(e.ExtractionSeq),
	); err != nil {
		return "", fmt.Errorf("evidence put: %w", err)
	}
	return id, nil
}

func (s *SQLiteEvidenceStore) PutRelation(rel model.EvidenceRelation) error {
	strength := clampStrength(rel.Strength)
	ctx := context.Background()
	const q = `INSERT INTO relations (from_id, to_id, kind, strength)
VALUES (?, ?, ?, ?)
ON CONFLICT(from_id, to_id, kind) DO UPDATE SET strength = excluded.strength`
	if _, err := s.db.ExecContext(ctx, q,
		string(rel.From),
		string(rel.To),
		string(rel.Kind),
		strength,
	); err != nil {
		return fmt.Errorf("relation put: %w", err)
	}
	return nil
}

func clampStrength(strength float64) float64 {
	if strength < 0 {
		return 0
	}
	if strength > 1 {
		return 1
	}
	return strength
}

func (s *SQLiteEvidenceStore) PutRelations(rels []model.EvidenceRelation) error {
	if len(rels) == 0 {
		return nil
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("relation put batch: begin: %w", err)
	}
	defer tx.Rollback()
	const q = `INSERT INTO relations (from_id, to_id, kind, strength)
VALUES (?, ?, ?, ?)
ON CONFLICT(from_id, to_id, kind) DO UPDATE SET strength = excluded.strength`
	for _, rel := range rels {
		strength := clampStrength(rel.Strength)
		if _, err := tx.ExecContext(ctx, q,
			string(rel.From),
			string(rel.To),
			string(rel.Kind),
			strength,
		); err != nil {
			return fmt.Errorf("relation put batch: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("relation put batch: commit: %w", err)
	}
	return nil
}

func (s *SQLiteEvidenceStore) Query(f EvidenceFilter) []model.Evidence {
	ctx := context.Background()
	const cols = `SELECT id, session_id, task_id, source_id, topic, claim, value, confidence, verification_state, collected_at, origin_url, extraction_seq FROM evidence`
	clauses := make([]string, 0, 3)
	args := make([]interface{}, 0, 3)
	if f.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, string(f.SessionID))
	}
	if f.Topic != "" {
		clauses = append(clauses, "topic = ?")
		args = append(args, f.Topic)
	}
	if f.Verification != "" {
		clauses = append(clauses, "verification_state = ?")
		args = append(args, string(f.Verification))
	}
	query := cols
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY collected_at ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []model.Evidence
	for rows.Next() {
		var e model.Evidence
		var id, sessionID, taskID, sourceID, verification string
		var topic, claim, value sql.NullString
		var confidence sql.NullFloat64
		var originURL sql.NullString
		var extractionSeq sql.NullInt64
		var collectedAt time.Time
		if err := rows.Scan(
			&id, &sessionID, &taskID, &sourceID,
			&topic, &claim, &value, &confidence,
			&verification, &collectedAt,
			&originURL, &extractionSeq,
		); err != nil {
			return nil
		}
		e.ID = model.EvidenceID(id)
		e.SessionID = model.SessionID(sessionID)
		e.TaskID = model.TaskID(taskID)
		e.SourceID = model.SourceID(sourceID)
		if topic.Valid {
			e.Topic = topic.String
		}
		if claim.Valid {
			e.Claim = claim.String
		}
		if value.Valid {
			e.Value = value.String
		}
		if confidence.Valid {
			e.Confidence = confidence.Float64
		}
		e.Verification = model.VerificationState(verification)
		e.CollectedAt = collectedAt
		if originURL.Valid {
			url := originURL.String
			e.OriginURL = &url
		}
		if extractionSeq.Valid {
			seq := int(extractionSeq.Int64)
			e.ExtractionSeq = &seq
		}
		out = append(out, e)
	}
	return out
}

func (s *SQLiteEvidenceStore) FindRelations(topic string) []model.EvidenceRelation {
	ctx := context.Background()
	const q = `SELECT DISTINCT r.from_id, r.to_id, r.kind, r.strength
FROM relations r
JOIN evidence e ON (r.from_id = e.id OR r.to_id = e.id)
WHERE e.topic = ?
ORDER BY r.from_id, r.to_id, r.kind`
	rows, err := s.db.QueryContext(ctx, q, topic)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []model.EvidenceRelation
	for rows.Next() {
		var rel model.EvidenceRelation
		var fromID, toID, kind string
		var strength sql.NullFloat64
		if err := rows.Scan(&fromID, &toID, &kind, &strength); err != nil {
			return nil
		}
		rel.From = model.EvidenceID(fromID)
		rel.To = model.EvidenceID(toID)
		rel.Kind = model.EvidenceRelationKind(kind)
		if strength.Valid {
			rel.Strength = strength.Float64
		}
		out = append(out, rel)
	}
	return out
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func ptrString(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

func ptrInt(i *int) interface{} {
	if i == nil {
		return nil
	}
	return *i
}

package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

const builtinOwner = "@builtin"

//go:embed content/quizzes.json
var builtinQuizData []byte

// Stable IDs make provisioning safe on every startup. The checked-in catalog is
// authoritative; personal copies and session snapshots are never overwritten.
func (s *server) provisionQuizzes() error {
	var catalog []Quiz
	if err := json.Unmarshal(builtinQuizData, &catalog); err != nil {
		return fmt.Errorf("read built-in quizzes: %w", err)
	}
	if len(catalog) != 10 {
		return fmt.Errorf("built-in catalog must have 10 quizzes")
	}
	seen := map[string]bool{}
	languages := map[string]int{}
	for i := range catalog {
		q := &catalog[i]
		if q.ID == "" || seen[q.ID] {
			return fmt.Errorf("missing or duplicate built-in quiz ID: %s", q.ID)
		}
		seen[q.ID] = true
		if err := validateQuiz(q); err != nil {
			return fmt.Errorf("built-in quiz %s: %w", q.ID, err)
		}
		languages[q.Language]++
		q.Builtin = true
	}
	if languages["uk"] != 8 || languages["en"] != 2 {
		return fmt.Errorf("built-in catalog needs 8 Ukrainian and 2 English quizzes")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range catalog {
		b, err := json.Marshal(q)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO quizzes(id,host,body) VALUES(?,?,?)
		ON CONFLICT(id) DO UPDATE SET body=excluded.body WHERE quizzes.host=excluded.host`, q.ID, builtinOwner, string(b))
		if err != nil {
			return fmt.Errorf("provision quiz %s: %w", q.ID, err)
		}
	}
	return tx.Commit()
}

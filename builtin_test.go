package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func TestBuiltinCatalog(t *testing.T) {
	var catalog []struct {
		Quiz
		Questions []struct {
			Question
			Source string `json:"source"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(builtinQuizData, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 10 {
		t.Fatalf("want 10 games, got %d", len(catalog))
	}
	languages := map[string]int{}
	prompts := map[string]bool{}
	ids := map[string]bool{}
	for _, entry := range catalog {
		if !strings.HasPrefix(entry.ID, "builtin-") || ids[entry.ID] {
			t.Fatalf("unstable or duplicate ID: %s", entry.ID)
		}
		ids[entry.ID] = true
		languages[entry.Language]++
		if len(entry.Questions) != 10 {
			t.Fatalf("%s needs 10 questions", entry.Title)
		}
		q := entry.Quiz
		q.Questions = nil
		for _, question := range entry.Questions {
			q.Questions = append(q.Questions, question.Question)
			if prompts[question.Prompt] {
				t.Fatalf("repeated question: %s", question.Prompt)
			}
			prompts[question.Prompt] = true
			if !strings.HasPrefix(question.Source, "https://") {
				t.Fatalf("question has no fact-check reference: %s", question.Prompt)
			}
		}
		if err := validateQuiz(&q); err != nil {
			t.Fatalf("%s: %v", q.Title, err)
		}
	}
	if languages["uk"] != 8 || languages["en"] != 2 {
		t.Fatalf("wrong language mix: %v", languages)
	}
}

func TestBuiltinsSharedButPersonalQuizzesAndSessionsPrivate(t *testing.T) {
	g := setup(t)
	first := g.request(t, g.host, "GET", "/bootstrap", "", nil, 200)
	if len(first["quizzes"].([]any)) != 10 {
		t.Fatal("new host has no built-in collection")
	}
	jar, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar}
	second := g.request(t, other, "GET", "/bootstrap", "", nil, 200)
	if len(second["quizzes"].([]any)) != 10 {
		t.Fatal("built-ins not shared")
	}
	for _, raw := range first["quizzes"].([]any) {
		q := raw.(map[string]any)
		id := q["id"].(string)
		if q["builtin"] != true {
			t.Fatal("built-in badge missing")
		}
		g.request(t, other, "PUT", "/quizzes/"+id, "", sampleQuiz(), 404)
		room := g.request(t, other, "POST", "/sessions", "", map[string]string{"quizId": id}, 201)
		g.request(t, g.host, "GET", "/sessions/"+room["code"].(string), "", nil, 403)
	}
	// Copying creates a private, editable quiz even if the client sends the
	// built-in marker and original ID back in its request.
	var catalog []Quiz
	if err := json.Unmarshal(builtinQuizData, &catalog); err != nil {
		t.Fatal(err)
	}
	copy := catalog[0]
	copy.Title = "My personal version"
	saved := g.request(t, g.host, "POST", "/quizzes", "", copy, 200)
	if saved["id"] == copy.ID || saved["builtin"] == true {
		t.Fatal("copy retained built-in identity")
	}
	copyID := saved["id"].(string)
	g.request(t, g.host, "PUT", "/quizzes/"+copyID, "", sampleQuiz(), 200)
	g.request(t, other, "POST", "/sessions", "", map[string]string{"quizId": copyID}, 404)
	second = g.request(t, other, "GET", "/bootstrap", "", nil, 200)
	if len(second["quizzes"].([]any)) != 10 {
		t.Fatal("personal copy leaked")
	}
}

func TestProvisioningRestoresCatalogWithoutOverwritingCopiesOrSessions(t *testing.T) {
	g := setup(t)
	var catalog []Quiz
	if err := json.Unmarshal(builtinQuizData, &catalog); err != nil {
		t.Fatal(err)
	}
	builtin := catalog[0]
	copy := g.request(t, g.host, "POST", "/quizzes", "", sampleQuiz(), 200)
	room := g.request(t, g.host, "POST", "/sessions", "", map[string]string{"quizId": builtin.ID}, 201)
	code := room["code"].(string)
	// Simulate an older installed catalog plus a missing built-in pack.
	if _, err := g.s.db.Exec("DELETE FROM quizzes WHERE id=?", catalog[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.s.db.Exec("UPDATE quizzes SET body=? WHERE id=?", `{"title":"Outdated built-in"}`, builtin.ID); err != nil {
		t.Fatal(err)
	}
	if err := g.s.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	g.s, err = openServer(g.path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = g.s.provisionQuizzes(); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = g.s.db.QueryRow("SELECT COUNT(*) FROM quizzes WHERE host=?", builtinOwner).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10 {
		t.Fatalf("startup duplicated or lost built-ins: %d", count)
	}
	var body string
	if err = g.s.db.QueryRow("SELECT body FROM quizzes WHERE id=?", copy["id"]).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var personal Quiz
	if err = json.Unmarshal([]byte(body), &personal); err != nil {
		t.Fatal(err)
	}
	if personal.Title != "Test game" {
		t.Fatal("provisioning overwrote a personal quiz")
	}
	if err = g.s.db.QueryRow("SELECT body FROM quizzes WHERE id=?", builtin.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var restored Quiz
	if err = json.Unmarshal([]byte(body), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Title != builtin.Title || !restored.Builtin {
		t.Fatal("startup did not refresh catalog")
	}
	x, err := g.s.load(code)
	if err != nil {
		t.Fatal(err)
	}
	if x.Quiz.Title != builtin.Title || x.Quiz.Questions[0].Correct != builtin.Questions[0].Correct {
		t.Fatal("startup changed an existing session snapshot")
	}
}

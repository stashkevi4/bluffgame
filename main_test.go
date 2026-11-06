package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testGame struct {
	s     *server
	http  *httptest.Server
	host  *http.Client
	path  string
	clock time.Time
}

func setup(t *testing.T) *testGame {
	t.Helper()
	path := filepath.Join(t.TempDir(), "game.db")
	s, err := openServer(path)
	if err != nil {
		t.Fatal(err)
	}
	g := &testGame{s: s, path: path, clock: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	s.now = func() time.Time { return g.clock }
	g.http = httptest.NewServer(s.handler())
	jar, _ := cookiejar.New(nil)
	g.host = &http.Client{Jar: jar}
	t.Cleanup(func() { g.http.Close(); g.s.db.Close() })
	return g
}
func (g *testGame) request(t *testing.T, client *http.Client, method, path, credential string, body any, status int) map[string]any {
	t.Helper()
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, g.http.URL+"/api"+path, &b)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var v map[string]any
	if err = json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != status {
		t.Fatalf("%s %s: expected %d, got %d: %v", method, path, status, res.StatusCode, v)
	}
	return v
}
func sampleQuiz() Quiz {
	q := Quiz{Title: "Test game", Questions: make([]Question, 10)}
	for i := range q.Questions {
		q.Questions[i] = Question{Prompt: fmt.Sprintf("Question %d?", i+1), Correct: "Truth", Lie: "Host lie"}
	}
	return q
}
func (g *testGame) create(t *testing.T) (string, string) {
	t.Helper()
	q := g.request(t, g.host, "POST", "/quizzes", "", sampleQuiz(), 200)
	id := q["id"].(string)
	x := g.request(t, g.host, "POST", "/sessions", "", map[string]string{"quizId": id}, 201)
	return x["code"].(string), id
}
func (g *testGame) join(t *testing.T, code, name string) string {
	t.Helper()
	v := g.request(t, nil, "POST", "/join", "", map[string]string{"code": code, "name": name}, 201)
	return v["token"].(string)
}
func opts(v map[string]any) []any { return v["options"].([]any) }

func TestFullGameScoringAndReplay(t *testing.T) {
	g := setup(t)
	code, quizID := g.create(t)
	base := "/sessions/" + code
	alice := g.join(t, code, "Alice")
	bob := g.join(t, code, "Bob")
	carol := g.join(t, code, "Carol")
	dave := g.join(t, code, "Dave")
	g.request(t, g.host, "POST", base+"/start", "", map[string]any{}, 409)
	for _, cred := range []string{alice, bob, carol, dave} {
		g.request(t, nil, "POST", base+"/ready", cred, map[string]any{}, 200)
	}
	g.request(t, nil, "POST", base+"/start", alice, map[string]any{}, 409)
	g.request(t, g.host, "POST", base+"/start", "", map[string]any{}, 200)
	g.request(t, nil, "POST", "/join", "", map[string]string{"code": code, "name": "Late"}, 409)
	for round := 1; round <= 10; round++ {
		g.request(t, nil, "POST", base+"/answer", alice, map[string]string{"answer": "Truth"}, 200)
		g.request(t, nil, "POST", base+"/answer", alice, map[string]string{"answer": "Changed"}, 409)
		g.request(t, nil, "POST", base+"/answer", bob, map[string]string{"answer": "Bob bluff"}, 200)
		g.request(t, nil, "POST", base+"/answer", dave, map[string]string{"answer": "Dave bluff"}, 200)
		private := g.request(t, nil, "GET", base, carol, nil, 200)
		if len(opts(private)) != 0 {
			t.Fatal("answers leaked before deadline")
		}
		if _, ok := private["correct"]; ok {
			t.Fatal("truth leaked")
		}
		g.clock = g.clock.Add(phaseLength - time.Nanosecond)
		if v := g.request(t, nil, "GET", base, alice, nil, 200); v["phase"] != "writing" {
			t.Fatal("writing ended early")
		}
		g.clock = g.clock.Add(time.Nanosecond)
		v := g.request(t, nil, "GET", base, carol, nil, 200)
		if v["phase"] != "voting" || len(opts(v)) != 5 {
			t.Fatalf("expected 5 options, got %v", v)
		}
		g.request(t, nil, "POST", base+"/answer", carol, map[string]string{"answer": "Too late"}, 409)
		for _, o := range opts(v) {
			item := o.(map[string]any)
			if _, ok := item["kind"]; ok {
				t.Fatal("option kind leaked")
			}
			if _, ok := item["authorId"]; ok {
				t.Fatal("author leaked")
			}
		}
		x, err := g.s.load(code)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]string{}
		for _, o := range x.Rounds[x.Index].Options {
			key := o.Kind
			if o.Text == "Bob bluff" {
				key = "bob"
			}
			ids[key] = o.ID
		}
		g.request(t, nil, "POST", base+"/vote", alice, map[string]string{"optionId": "unknown"}, 409)
		// Find Alice's duplicated truth by author rather than its text.
		aliceID := x.Players[0].ID
		var duplicate string
		for _, o := range x.Rounds[x.Index].Options {
			if o.AuthorID == aliceID {
				duplicate = o.ID
			}
		}
		g.request(t, nil, "POST", base+"/vote", alice, map[string]string{"optionId": duplicate}, 409)
		for _, pair := range [][2]string{{alice, ids["bob"]}, {bob, ids["correct"]}, {carol, duplicate}, {dave, ids["host"]}} {
			g.request(t, nil, "POST", base+"/vote", pair[0], map[string]string{"optionId": pair[1]}, 200)
		}
		g.request(t, nil, "POST", base+"/vote", alice, map[string]string{"optionId": ids["correct"]}, 409)
		g.request(t, g.host, "POST", base+"/next", "", map[string]any{}, 409)
		g.clock = g.clock.Add(phaseLength)
		v = g.request(t, nil, "GET", base, alice, nil, 200)
		if v["phase"] != "reveal" {
			t.Fatal("round not revealed")
		}
		if v["revealComplete"] != false || len(v["results"].([]any)) != 0 {
			t.Fatal("results revealed automatically")
		}
		for _, raw := range v["players"].([]any) {
			p := raw.(map[string]any)
			previous := map[string]int{"Alice": round - 1, "Bob": (round - 1) * 3, "Carol": 0, "Dave": -(round - 1)}
			if int(p["score"].(float64)) != previous[p["name"].(string)] {
				t.Fatal("points awarded before host reveal")
			}
		}
		for _, o := range opts(v) {
			if _, ok := o.(map[string]any)["kind"]; ok {
				t.Fatal("option authors leaked before reveal completion")
			}
		}
		g.request(t, nil, "POST", base+"/reveal", alice, map[string]string{"playerId": x.Players[3].ID}, 409)
		g.request(t, g.host, "POST", base+"/reveal", "", map[string]string{"playerId": "missing"}, 409)
		g.request(t, g.host, "POST", base+"/next", "", map[string]any{}, 409)
		// The host chooses the order, including the player who missed writing.
		for i, playerIndex := range []int{3, 2, 0, 1} {
			v = g.request(t, g.host, "POST", base+"/reveal", "", map[string]string{"playerId": x.Players[playerIndex].ID}, 200)
			results := v["results"].([]any)
			if len(results) != i+1 || results[i].(map[string]any)["playerId"] != x.Players[playerIndex].ID {
				t.Fatal("reveal order was not preserved")
			}
			g.request(t, g.host, "POST", base+"/reveal", "", map[string]string{"playerId": x.Players[playerIndex].ID}, 409)
			if i < 3 {
				g.request(t, g.host, "POST", base+"/next", "", map[string]any{}, 409)
			}
			if i == 0 {
				if results[0].(map[string]any)["kind"] != "host" || results[0].(map[string]any)["points"] != float64(-1) {
					t.Fatal("host lie result incorrect")
				}
				for _, p := range v["players"].([]any) {
					if p.(map[string]any)["name"] == "Bob" && p.(map[string]any)["score"] != float64((round-1)*3) {
						t.Fatal("unrevealed vote scored early")
					}
				}
			}
			if i == 1 {
				if results[1].(map[string]any)["authorId"] != aliceID || results[1].(map[string]any)["authorPoints"] != float64(1) {
					t.Fatal("duplicate did not award author on reveal")
				}
			}
		}
		if v["revealComplete"] != true || v["correct"] != "Truth" {
			t.Fatal("completed reveal did not show truth")
		}
		want := map[string]int{"Alice": round, "Bob": round * 3, "Carol": 0, "Dave": -round}
		for _, raw := range v["players"].([]any) {
			p := raw.(map[string]any)
			if int(p["score"].(float64)) != want[p["name"].(string)] {
				t.Fatalf("wrong score: %v; want %v", p, want)
			}
		}
		for i := 0; i < 3; i++ {
			again := g.request(t, g.host, "GET", base, "", nil, 200)
			if again["players"].([]any)[0].(map[string]any)["score"] != float64(round*3) {
				t.Fatal("round scored twice")
			}
		}
		g.request(t, nil, "POST", base+"/next", alice, map[string]any{}, 409)
		v = g.request(t, g.host, "POST", base+"/next", "", map[string]any{}, 200)
		if round == 10 && v["phase"] != "finished" {
			t.Fatal("game not finished")
		}
	}
	replayed := g.request(t, g.host, "POST", "/sessions", "", map[string]string{"quizId": quizID}, 201)
	if replayed["code"] == code {
		t.Fatal("replay reused old room")
	}
	newRoom := g.request(t, g.host, "GET", "/sessions/"+replayed["code"].(string), "", nil, 200)
	if newRoom["phase"] != "lobby" || len(newRoom["players"].([]any)) != 0 {
		t.Fatal("replay did not reset")
	}
}

func TestRestartAndOverdueDeadlines(t *testing.T) {
	g := setup(t)
	code, _ := g.create(t)
	base := "/sessions/" + code
	cred := g.join(t, code, "Alice")
	g.request(t, nil, "POST", base+"/ready", cred, map[string]any{}, 200)
	g.request(t, g.host, "POST", base+"/start", "", map[string]any{}, 200)
	g.request(t, nil, "POST", base+"/answer", cred, map[string]string{"answer": "Persistent bluff"}, 200)
	g.http.Close()
	g.s.db.Close()
	newServer, err := openServer(g.path)
	if err != nil {
		t.Fatal(err)
	}
	g.clock = g.clock.Add(61 * time.Second)
	newServer.now = func() time.Time { return g.clock }
	g.s = newServer
	g.http = httptest.NewServer(newServer.handler())
	v := g.request(t, nil, "GET", base, cred, nil, 200)
	if v["phase"] != "reveal" || v["myAnswer"] != "Persistent bluff" {
		t.Fatalf("restart lost state or extended deadline: %v", v)
	}
	g.request(t, nil, "POST", base+"/vote", cred, map[string]string{"optionId": "anything"}, 409)
	if len(opts(v)) != 3 {
		t.Fatal("persisted answer missing")
	}
}

func TestValidationPrivacyAndQuizSnapshot(t *testing.T) {
	g := setup(t)
	bad := sampleQuiz()
	bad.Questions = bad.Questions[:9]
	g.request(t, g.host, "POST", "/quizzes", "", bad, 400)
	bad = sampleQuiz()
	bad.Questions[1].Lie = " truth "
	g.request(t, g.host, "POST", "/quizzes", "", bad, 400)
	code, id := g.create(t)
	base := "/sessions/" + code
	outsider := &http.Client{}
	g.request(t, outsider, "GET", base, "", nil, 403)
	g.request(t, outsider, "PUT", "/quizzes/"+id, "", sampleQuiz(), 404)
	g.request(t, outsider, "POST", "/sessions", "", map[string]string{"quizId": id}, 404)
	bootstrap := g.request(t, outsider, "GET", "/bootstrap", "", nil, 200)
	if len(bootstrap["quizzes"].([]any)) != 10 {
		t.Fatal("new host should see the 10 built-in quizzes")
	}
	for _, raw := range bootstrap["quizzes"].([]any) {
		q := raw.(map[string]any)
		if q["builtin"] != true || q["id"] == id {
			t.Fatal("personal quiz leaked to another host")
		}
	}
	cred := g.join(t, code, "Alice")
	g.request(t, nil, "POST", "/join", "", map[string]string{"code": code, "name": " alice "}, 409)
	g.request(t, nil, "POST", base+"/answer", cred, map[string]string{"answer": "lobby answer"}, 409)
	changed := sampleQuiz()
	changed.Title = "Edited quiz"
	changed.Questions[0].Correct = "New truth"
	g.request(t, g.host, "PUT", "/quizzes/"+id, "", changed, 200)
	x, err := g.s.load(code)
	if err != nil {
		t.Fatal(err)
	}
	if x.Quiz.Title == changed.Title || x.Quiz.Questions[0].Correct != "Truth" {
		t.Fatal("editing quiz modified running snapshot")
	}
	v := g.request(t, nil, "GET", base, cred, nil, 200)
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), cred) || strings.Contains(string(b), x.Host) || strings.Contains(string(b), "Host lie") {
		t.Fatal("secret leaked to player")
	}
	// The host cannot submit answers or votes.
	g.request(t, g.host, "POST", base+"/answer", "", map[string]string{"answer": "host"}, 409)
}

func TestConcurrentAnswerIsLockedOnce(t *testing.T) {
	g := setup(t)
	code, _ := g.create(t)
	base := "/sessions/" + code
	cred := g.join(t, code, "Alice")
	g.request(t, nil, "POST", base+"/ready", cred, map[string]any{}, 200)
	g.request(t, g.host, "POST", base+"/start", "", map[string]any{}, 200)
	var wg sync.WaitGroup
	statuses := make(chan int, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", g.http.URL+"/api"+base+"/answer", strings.NewReader(`{"answer":"One answer"}`))
			req.Header.Set("Authorization", "Bearer "+cred)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			defer res.Body.Close()
			statuses <- res.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	success := 0
	for status := range statuses {
		if status == 200 {
			success++
		} else if status != 409 {
			t.Fatalf("unexpected status %d", status)
		}
	}
	if success != 1 {
		t.Fatalf("expected one accepted answer, got %d", success)
	}
}

type delayedBody struct {
	io.Reader
	onRead func()
}

func (b delayedBody) Read(p []byte) (int, error) {
	b.onRead()
	return b.Reader.Read(p)
}

func TestBodyMustFinishBeforeDeadline(t *testing.T) {
	g := setup(t)
	code, _ := g.create(t)
	base := "/sessions/" + code
	cred := g.join(t, code, "Alice")
	g.request(t, nil, "POST", base+"/ready", cred, map[string]any{}, 200)
	g.request(t, g.host, "POST", base+"/start", "", map[string]any{}, 200)
	x, err := g.s.load(code)
	if err != nil {
		t.Fatal(err)
	}
	// The request arrives in time, but its body completes exactly at the deadline.
	body := delayedBody{Reader: strings.NewReader(`{"answer":"Too late"}`), onRead: func() { g.clock = x.Deadline }}
	req := httptest.NewRequest("POST", base+"/answer", body)
	if err = g.s.act(httptest.NewRecorder(), req, x, x.Players[0], false, "answer"); err == nil {
		t.Fatal("accepted an answer whose body completed at the deadline")
	}
	if len(x.Rounds[0].Answers) != 0 {
		t.Fatal("late body mutated the round")
	}
	g.s.advance(x)
	correct := ""
	for _, o := range x.Rounds[0].Options {
		if o.Kind == "correct" {
			correct = o.ID
		}
	}
	body = delayedBody{Reader: strings.NewReader(fmt.Sprintf(`{"optionId":%q}`, correct)), onRead: func() { g.clock = x.Deadline }}
	req = httptest.NewRequest("POST", base+"/vote", body)
	if err = g.s.act(httptest.NewRecorder(), req, x, x.Players[0], false, "vote"); err == nil {
		t.Fatal("accepted a vote whose body completed at the deadline")
	}
	if len(x.Rounds[0].Votes) != 0 {
		t.Fatal("late body mutated the votes")
	}
}

func TestPartialRevealSurvivesRestart(t *testing.T) {
	g := setup(t)
	code, _ := g.create(t)
	base := "/sessions/" + code
	alice := g.join(t, code, "Оля")
	bob := g.join(t, code, "Богдан")
	for _, cred := range []string{alice, bob} {
		g.request(t, nil, "POST", base+"/ready", cred, map[string]any{}, 200)
	}
	g.request(t, g.host, "POST", base+"/start", "", map[string]any{}, 200)
	g.request(t, nil, "POST", base+"/answer", alice, map[string]string{"answer": "Вигадка Олі"}, 200)
	g.request(t, g.host, "POST", base+"/reveal", "", map[string]string{"playerId": "anything"}, 409)
	g.clock = g.clock.Add(phaseLength)
	g.request(t, nil, "GET", base, bob, nil, 200)
	x, err := g.s.load(code)
	if err != nil {
		t.Fatal(err)
	}
	var correct, lie string
	for _, option := range x.Rounds[0].Options {
		if option.Kind == "correct" {
			correct = option.ID
		}
		if option.Kind == "player" {
			lie = option.ID
		}
	}
	g.request(t, nil, "POST", base+"/vote", alice, map[string]string{"optionId": correct}, 200)
	g.request(t, nil, "POST", base+"/vote", bob, map[string]string{"optionId": lie}, 200)
	g.clock = g.clock.Add(phaseLength)
	g.request(t, g.host, "POST", base+"/reveal", "", map[string]string{"playerId": x.Players[1].ID}, 200)
	v := g.request(t, nil, "GET", base, alice, nil, 200)
	if v["players"].([]any)[0].(map[string]any)["score"] != float64(1) {
		t.Fatal("author did not receive +1 at the reveal step")
	}
	if len(v["results"].([]any)) != 1 {
		t.Fatal("unselected player's vote leaked")
	}
	if _, ok := v["correct"]; ok {
		t.Fatal("full truth summary exposed before completion")
	}
	g.http.Close()
	g.s.db.Close()
	g.s, err = openServer(g.path)
	if err != nil {
		t.Fatal(err)
	}
	g.s.now = func() time.Time { return g.clock }
	g.http = httptest.NewServer(g.s.handler())
	v = g.request(t, nil, "GET", base, alice, nil, 200)
	if len(v["results"].([]any)) != 1 || v["revealComplete"] != false {
		t.Fatal("partial reveal lost after restart")
	}
	g.request(t, g.host, "POST", base+"/reveal", "", map[string]string{"playerId": x.Players[1].ID}, 409)
	v = g.request(t, g.host, "POST", base+"/reveal", "", map[string]string{"playerId": x.Players[0].ID}, 200)
	if v["players"].([]any)[0].(map[string]any)["score"] != float64(3) || v["revealComplete"] != true {
		t.Fatal("restart duplicated or lost points")
	}
}

func TestConcurrentRevealAndMissedVote(t *testing.T) {
	g := setup(t)
	code, _ := g.create(t)
	base := "/sessions/" + code
	cred := g.join(t, code, "Alice")
	g.request(t, nil, "POST", base+"/ready", cred, map[string]any{}, 200)
	g.request(t, g.host, "POST", base+"/start", "", map[string]any{}, 200)
	g.clock = g.clock.Add(2 * phaseLength)
	g.request(t, nil, "GET", base, cred, nil, 200)
	x, err := g.s.load(code)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", g.http.URL+"/api"+base+"/reveal", strings.NewReader(fmt.Sprintf(`{"playerId":%q}`, x.Players[0].ID)))
			res, err := g.host.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			defer res.Body.Close()
			statuses <- res.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	accepted := 0
	for status := range statuses {
		if status == 200 {
			accepted++
		} else if status != 409 {
			t.Fatalf("unexpected status %d", status)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent reveals", accepted)
	}
	v := g.request(t, nil, "GET", base, cred, nil, 200)
	result := v["results"].([]any)[0].(map[string]any)
	if result["kind"] != "none" || result["points"] != float64(0) || v["revealComplete"] != true {
		t.Fatal("missed vote was not revealed as zero")
	}
}

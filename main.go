package main

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

//go:embed web/*
var assets embed.FS

const phaseLength = 30 * time.Second

type Question struct {
	Prompt  string `json:"prompt"`
	Correct string `json:"correct"`
	Lie     string `json:"lie"`
}
type Quiz struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Questions []Question `json:"questions"`
	CreatedAt time.Time  `json:"createdAt"`
	Builtin   bool       `json:"builtin,omitempty"`
	Language  string     `json:"language,omitempty"`
}
type Player struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	Score int    `json:"score"`
}
type Option struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Kind     string `json:"kind"`
	AuthorID string `json:"authorId,omitempty"`
}
type Round struct {
	Answers map[string]string `json:"answers"`
	Votes   map[string]string `json:"votes"`
	Options []Option          `json:"options"`
	Changes map[string]int    `json:"changes"`
	// Nil on old, already-scored rounds. New rounds persist reveal order.
	Revealed     []string `json:"revealed,omitempty"`
	ManualReveal bool     `json:"manualReveal,omitempty"`
}
type Session struct {
	Code     string    `json:"code"`
	Host     string    `json:"host"`
	Quiz     Quiz      `json:"quiz"`
	Phase    string    `json:"phase"`
	Index    int       `json:"index"`
	Deadline time.Time `json:"deadline"`
	Players  []*Player `json:"players"`
	Rounds   []*Round  `json:"rounds"`
}
type server struct {
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
}

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func openServer(path string) (*server, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
	CREATE TABLE IF NOT EXISTS quizzes (id TEXT PRIMARY KEY, host TEXT NOT NULL, body TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS sessions (code TEXT PRIMARY KEY, host TEXT NOT NULL, body TEXT NOT NULL);
	CREATE INDEX IF NOT EXISTS quiz_host ON quizzes(host);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &server{db: db, now: time.Now}
	if err := s.provisionQuizzes(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func main() {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "data/varianty.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Fatal(err)
	}
	s, err := openServer(path)
	if err != nil {
		log.Fatal(err)
	}
	defer s.db.Close()
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("Options is ready at http://localhost%s (listen %s)", addr, addr)
	h := &http.Server{Addr: addr, Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(h.ListenAndServe())
}
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", s.api)
	files, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("Please send valid JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("Please send a single JSON object")
	}
	return nil
}
func hostID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("options_host"); err == nil && len(c.Value) == 48 {
		return c.Value
	}
	id := token()
	http.SetCookie(w, &http.Cookie{Name: "options_host", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 365 * 86400})
	return id
}
func validText(t string, max int) bool { n := utf8.RuneCountInString(t); return n > 0 && n <= max }
func validateQuiz(q *Quiz) error {
	q.Title = strings.TrimSpace(q.Title)
	if !validText(q.Title, 80) {
		return errors.New("Give your quiz a title (up to 80 characters)")
	}
	if len(q.Questions) != 10 {
		return errors.New("A quiz needs exactly 10 questions")
	}
	for i := range q.Questions {
		x := &q.Questions[i]
		x.Prompt = strings.TrimSpace(x.Prompt)
		x.Correct = strings.TrimSpace(x.Correct)
		x.Lie = strings.TrimSpace(x.Lie)
		if !validText(x.Prompt, 300) || !validText(x.Correct, 100) || !validText(x.Lie, 100) {
			return fmt.Errorf("Question %d needs a prompt (up to 300 characters), a correct answer and a host lie (up to 100 each)", i+1)
		}
		if strings.EqualFold(x.Correct, x.Lie) {
			return fmt.Errorf("Question %d: the host lie must differ from the correct answer", i+1)
		}
	}
	return nil
}
func (s *server) api(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, "Open the game directly to continue")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	host := hostID(w, r)
	path := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.URL.Path == "/api/bootstrap" && r.Method == "GET" {
		rows, err := s.db.Query("SELECT body FROM quizzes WHERE host=? OR host=? ORDER BY CASE WHEN host=? THEN 0 ELSE 1 END, rowid", host, builtinOwner, host)
		if err != nil {
			s.internal(w, err)
			return
		}
		defer rows.Close()
		quizzes := []Quiz{}
		for rows.Next() {
			var b string
			var q Quiz
			if err = rows.Scan(&b); err != nil {
				s.internal(w, err)
				return
			}
			if err = json.Unmarshal([]byte(b), &q); err != nil {
				s.internal(w, err)
				return
			}
			quizzes = append(quizzes, q)
		}
		if err = rows.Err(); err != nil {
			s.internal(w, err)
			return
		}
		reply(w, 200, map[string]any{"quizzes": quizzes})
		return
	}
	if len(path) >= 2 && path[1] == "quizzes" && (r.Method == "POST" || r.Method == "PUT") {
		if (r.Method == "POST" && len(path) != 2) || (r.Method == "PUT" && len(path) != 3) {
			fail(w, 404, "Not found")
			return
		}
		var q Quiz
		if err := decode(w, r, &q); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if err := validateQuiz(&q); err != nil {
			fail(w, 400, err.Error())
			return
		}
		q.Builtin = false
		if r.Method == "POST" {
			q.ID = token()
			q.CreatedAt = s.now()
		} else {
			var body string
			if err := s.db.QueryRow("SELECT body FROM quizzes WHERE id=? AND host=?", path[2], host).Scan(&body); err != nil {
				fail(w, 404, "Quiz not found")
				return
			}
			var old Quiz
			if err := json.Unmarshal([]byte(body), &old); err != nil {
				s.internal(w, err)
				return
			}
			q.ID = old.ID
			q.CreatedAt = old.CreatedAt
		}
		b, _ := json.Marshal(q)
		if _, err := s.db.Exec("INSERT INTO quizzes(id,host,body) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", q.ID, host, string(b)); err != nil {
			s.internal(w, err)
			return
		}
		reply(w, 200, q)
		return
	}
	if r.URL.Path == "/api/sessions" && r.Method == "POST" {
		var input struct {
			QuizID string `json:"quizId"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err.Error())
			return
		}
		var b string
		var q Quiz
		if err := s.db.QueryRow("SELECT body FROM quizzes WHERE id=? AND (host=? OR host=?)", input.QuizID, host, builtinOwner).Scan(&b); err != nil {
			fail(w, 404, "Choose one of your quizzes first")
			return
		}
		if err := json.Unmarshal([]byte(b), &q); err != nil {
			s.internal(w, err)
			return
		}
		code, err := s.newCode()
		if err != nil {
			s.internal(w, err)
			return
		}
		x := &Session{Code: code, Host: host, Quiz: q, Phase: "lobby", Index: -1, Players: []*Player{}, Rounds: []*Round{}}
		if err = s.save(x); err != nil {
			s.internal(w, err)
			return
		}
		reply(w, 201, map[string]string{"code": code})
		return
	}
	if r.URL.Path == "/api/join" && r.Method == "POST" {
		var input struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err.Error())
			return
		}
		x, err := s.load(strings.ToUpper(strings.TrimSpace(input.Code)))
		if err != nil {
			fail(w, 404, "That room doesn't exist. Check the room code.")
			return
		}
		if x.Phase != "lobby" {
			fail(w, 409, "This game has already started")
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if !validText(input.Name, 24) {
			fail(w, 400, "Enter a name with 1–24 characters")
			return
		}
		for _, p := range x.Players {
			if strings.EqualFold(p.Name, input.Name) {
				fail(w, 409, "That name is taken. Try another one.")
				return
			}
		}
		if len(x.Players) >= 20 {
			fail(w, 409, "This room is full (20 players)")
			return
		}
		p := &Player{ID: token(), Token: token(), Name: input.Name}
		x.Players = append(x.Players, p)
		if err = s.save(x); err != nil {
			s.internal(w, err)
			return
		}
		reply(w, 201, map[string]string{"code": x.Code, "token": p.Token})
		return
	}
	if len(path) >= 3 && path[1] == "sessions" {
		x, err := s.load(strings.ToUpper(path[2]))
		if err != nil {
			fail(w, 404, "Room not found")
			return
		}
		var me *Player
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		for _, p := range x.Players {
			if p.Token == bearer {
				me = p
				break
			}
		}
		isHost := bearer == "" && host == x.Host
		if !isHost && me == nil {
			fail(w, 403, "Join this room to see the game")
			return
		}
		if s.advance(x) {
			if err = s.save(x); err != nil {
				s.internal(w, err)
				return
			}
		}
		if len(path) == 3 && r.Method == "GET" {
			reply(w, 200, s.view(x, me, isHost))
			return
		}
		if len(path) != 4 || r.Method != "POST" {
			fail(w, 404, "Not found")
			return
		}
		if err = s.act(w, r, x, me, isHost, path[3]); err != nil {
			fail(w, 409, err.Error())
			return
		}
		if err = s.save(x); err != nil {
			s.internal(w, err)
			return
		}
		reply(w, 200, s.view(x, me, isHost))
		return
	}
	fail(w, 404, "Not found")
}
func (s *server) internal(w http.ResponseWriter, err error) {
	log.Printf("database error: %v", err)
	fail(w, 500, "Something went wrong. Please try again.")
}
func (s *server) newCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for attempts := 0; attempts < 20; attempts++ {
		b := make([]byte, 5)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			b[i] = alphabet[n.Int64()]
		}
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE code=?", string(b)).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return string(b), nil
		}
	}
	return "", errors.New("could not generate room code")
}
func (s *server) load(code string) (*Session, error) {
	var b string
	if err := s.db.QueryRow("SELECT body FROM sessions WHERE code=?", code).Scan(&b); err != nil {
		return nil, err
	}
	var x Session
	err := json.Unmarshal([]byte(b), &x)
	return &x, err
}
func (s *server) save(x *Session) error {
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO sessions(code,host,body) VALUES(?,?,?) ON CONFLICT(code) DO UPDATE SET body=excluded.body", x.Code, x.Host, string(b))
	return err
}
func (s *server) startRound(x *Session) {
	x.Index++
	x.Phase = "writing"
	x.Deadline = s.now().Add(phaseLength)
	x.Rounds = append(x.Rounds, &Round{Answers: map[string]string{}, Votes: map[string]string{}, Options: []Option{}, Changes: map[string]int{}, Revealed: []string{}, ManualReveal: true})
}
func (s *server) advance(x *Session) bool {
	changed := false
	for (x.Phase == "writing" || x.Phase == "voting") && !s.now().Before(x.Deadline) {
		r := x.Rounds[x.Index]
		if x.Phase == "writing" {
			q := x.Quiz.Questions[x.Index]
			r.Options = []Option{{ID: token(), Text: q.Correct, Kind: "correct"}, {ID: token(), Text: q.Lie, Kind: "host"}}
			for _, p := range x.Players {
				if a, ok := r.Answers[p.ID]; ok {
					r.Options = append(r.Options, Option{ID: token(), Text: a, Kind: "player", AuthorID: p.ID})
				}
			}
			for i := len(r.Options) - 1; i > 0; i-- {
				j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
				if err != nil {
					panic(err)
				}
				r.Options[i], r.Options[j.Int64()] = r.Options[j.Int64()], r.Options[i]
			}
			x.Phase = "voting"
			x.Deadline = x.Deadline.Add(phaseLength)
		} else {
			// Old timed rounds also adopt manual reveals. Previously revealed
			// rounds retain their existing scores for database compatibility.
			r.ManualReveal = true
			x.Phase = "reveal"
			x.Deadline = time.Time{}
		}
		changed = true
	}
	return changed
}
func revealPlayer(x *Session, playerID string) {
	r := x.Rounds[x.Index]
	changes := map[string]int{}
	if optionID, ok := r.Votes[playerID]; ok {
		for _, o := range r.Options {
			if o.ID != optionID {
				continue
			}
			switch o.Kind {
			case "correct":
				changes[playerID] += 2
			case "host":
				changes[playerID]--
			case "player":
				changes[o.AuthorID]++
			}
			break
		}
	}
	for _, p := range x.Players {
		p.Score += changes[p.ID]
		r.Changes[p.ID] += changes[p.ID]
	}
	r.Revealed = append(r.Revealed, playerID)
}

func allRevealed(x *Session) bool {
	r := x.Rounds[x.Index]
	return !r.ManualReveal || len(r.Revealed) == len(x.Players)
}
func (s *server) act(w http.ResponseWriter, r *http.Request, x *Session, me *Player, host bool, action string) error {
	switch action {
	case "start", "next":
		if !host {
			return errors.New("Only the host can start a question")
		}
		if action == "start" {
			if x.Phase != "lobby" {
				return errors.New("The game has already started")
			}
			if len(x.Players) == 0 {
				return errors.New("Wait for at least one player to join")
			}
			for _, p := range x.Players {
				if !p.Ready {
					return errors.New("Wait until every player is ready")
				}
			}
		} else if x.Phase != "reveal" {
			return errors.New("Wait for this question to finish")
		} else if !allRevealed(x) {
			return errors.New("Reveal every player's result before the next question")
		}
		if x.Index == 9 {
			x.Phase = "finished"
		} else {
			s.startRound(x)
		}
	case "reveal":
		if !host {
			return errors.New("Only the host can reveal results")
		}
		if x.Phase != "reveal" {
			return errors.New("Wait for voting to finish")
		}
		var input struct {
			PlayerID string `json:"playerId"`
		}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		round := x.Rounds[x.Index]
		if !round.ManualReveal {
			return errors.New("This round has already been revealed")
		}
		for _, id := range round.Revealed {
			if id == input.PlayerID {
				return errors.New("This player's result is already revealed")
			}
		}
		found := false
		for _, p := range x.Players {
			if p.ID == input.PlayerID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("Choose a player in this room")
		}
		revealPlayer(x, input.PlayerID)
	case "ready":
		if me == nil || x.Phase != "lobby" {
			return errors.New("Players can get ready in the lobby")
		}
		me.Ready = !me.Ready
	case "answer":
		if me == nil || x.Phase != "writing" {
			return errors.New("The answer window is closed")
		}
		var input struct {
			Answer string `json:"answer"`
		}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		if !s.now().Before(x.Deadline) {
			return errors.New("The answer window is closed")
		}
		input.Answer = strings.TrimSpace(input.Answer)
		if !validText(input.Answer, 100) {
			return errors.New("Write an answer with 1–100 characters")
		}
		round := x.Rounds[x.Index]
		if _, ok := round.Answers[me.ID]; ok {
			return errors.New("Your answer is already locked in")
		}
		round.Answers[me.ID] = input.Answer
	case "vote":
		if me == nil || x.Phase != "voting" {
			return errors.New("The voting window is closed")
		}
		var input struct {
			OptionID string `json:"optionId"`
		}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		if !s.now().Before(x.Deadline) {
			return errors.New("The voting window is closed")
		}
		round := x.Rounds[x.Index]
		if _, ok := round.Votes[me.ID]; ok {
			return errors.New("Your vote is already locked in")
		}
		found := false
		for _, o := range round.Options {
			if o.ID == input.OptionID {
				if o.AuthorID == me.ID {
					return errors.New("Choose an option other than your own answer")
				}
				found = true
				break
			}
		}
		if !found {
			return errors.New("Choose one of the available options")
		}
		round.Votes[me.ID] = input.OptionID
	default:
		return errors.New("Unknown game action")
	}
	return nil
}
func (s *server) view(x *Session, me *Player, host bool) map[string]any {
	v := map[string]any{"code": x.Code, "title": x.Quiz.Title, "quizId": x.Quiz.ID, "phase": x.Phase, "questionNumber": x.Index + 1, "totalQuestions": 10, "serverTime": s.now(), "deadline": x.Deadline, "isHost": host}
	players := []map[string]any{}
	for _, p := range x.Players {
		players = append(players, map[string]any{"id": p.ID, "name": p.Name, "ready": p.Ready, "score": p.Score})
	}
	sort.SliceStable(players, func(i, j int) bool { return players[i]["score"].(int) > players[j]["score"].(int) })
	v["players"] = players
	if me != nil {
		v["me"] = me.ID
	}
	if x.Index >= 0 {
		r := x.Rounds[x.Index]
		v["prompt"] = x.Quiz.Questions[x.Index].Prompt
		v["answerCount"] = len(r.Answers)
		v["voteCount"] = len(r.Votes)
		revealed := map[string]bool{}
		for _, id := range r.Revealed {
			revealed[id] = true
		}
		if !r.ManualReveal && (x.Phase == "reveal" || x.Phase == "finished") {
			for _, p := range x.Players {
				revealed[p.ID] = true
			}
		}
		complete := (x.Phase == "reveal" || x.Phase == "finished") && allRevealed(x)
		if me != nil {
			v["myAnswer"] = r.Answers[me.ID]
			v["myVote"] = r.Votes[me.ID]
		}
		options := []map[string]any{}
		if x.Phase != "writing" {
			for _, o := range r.Options {
				item := map[string]any{"id": o.ID, "text": o.Text, "isMine": me != nil && o.AuthorID == me.ID}
				if complete {
					item["kind"] = o.Kind
					item["authorId"] = o.AuthorID
					voters := []string{}
					for _, p := range x.Players {
						if revealed[p.ID] && r.Votes[p.ID] == o.ID {
							voters = append(voters, p.ID)
						}
					}
					item["voters"] = voters
				}
				options = append(options, item)
			}
		}
		v["options"] = options
		if x.Phase == "reveal" || x.Phase == "finished" {
			v["changes"] = r.Changes
			v["revealComplete"] = complete
			results := []map[string]any{}
			order := r.Revealed
			if !r.ManualReveal {
				order = []string{}
				for _, p := range x.Players {
					order = append(order, p.ID)
				}
			}
			for _, id := range order {
				result := map[string]any{"playerId": id, "kind": "none", "points": 0}
				for _, o := range r.Options {
					if r.Votes[id] != o.ID {
						continue
					}
					result["optionId"] = o.ID
					result["text"] = o.Text
					result["kind"] = o.Kind
					switch o.Kind {
					case "correct":
						result["points"] = 2
					case "host":
						result["points"] = -1
					case "player":
						result["authorId"] = o.AuthorID
						result["authorPoints"] = 1
					}
					break
				}
				results = append(results, result)
			}
			v["results"] = results
			if complete {
				v["correct"] = x.Quiz.Questions[x.Index].Correct
			}
		}
	}
	return v
}

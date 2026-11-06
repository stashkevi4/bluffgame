# Options

A mobile-friendly bluffing game for friends, served by Go with an embedded SQLite database. One host runs the room; players use their own browsers. No frontend build step or external service is needed. CSS, JavaScript and HTML are embedded in the Go executable.

## Run

Requires Go 1.24 or newer. The first build downloads the pure-Go SQLite driver.

```sh
go run .
```

Open http://localhost:8080. Phones on the same Wi-Fi should open `http://YOUR_COMPUTER_LAN_IP:8080`. Share that address or use **Copy invite** after opening the host page through the LAN address. A localhost invite only works on the host computer. Allow port 8080 through the local firewall if needed.

```sh
# Choose an address and database location.
HTTP_ADDR=:8080 DB_PATH=data/varianty.db go run .

# Build a standalone executable, with all web assets included.
go build -o varianty .
./varianty

# Verify gameplay, persistence, permissions, and concurrent submissions.
go test -race ./...

# Verify language handling and reveal notifications (requires Node.js).
node --test scripts/i18n.test.cjs
```

## Play

1. The host picks a built-in game or creates a quiz with **exactly 10 questions**, each with a correct answer and a different host lie. Use **Make a copy** to customize a built-in game.
2. Start a room. Players enter its five-character code and a unique name. Up to 20 players can join before the game starts.
3. Every player marks themselves ready. The host starts question one.
4. **Writing: 30 seconds.** Each player may submit one answer. Answers remain private until the deadline. Missing this window means no option is added for that player; they can still vote.
5. **Voting: 30 seconds.** The official answer, host lie and all submitted answers appear in a shuffled list. Each player can vote once, except for their own submission.
6. Voting ends without exposing votes or adding points. The host selects players in any order to reveal their choices. Each click awards +2 for the official truth, −1 for the host lie, or +1 to the author of a selected player bluff. A missed vote reveals as 0. Everyone sees the latest reveal and the live standings; the bluff author gets a personal +1 notification. The host can continue only after every player is revealed.
7. After question 10, the host opens the final results. Ties share the win. Replay creates a fresh room with scores reset; the quiz is reusable.

Use the **EN / УКР** selector to choose English or Ukrainian independently on each device. The choice survives reloads. Menus, rules, game screens, errors and starter questions are translated. Existing quiz content, names and answers remain as written. Switching language preserves in-progress form entries and votes.

Both phases last their full 30 seconds, even if everyone submits early. Deadlines use the server clock. Clients poll once per second and recover on reconnect. An expired phase advances when the server next receives a room request; a delayed request never extends either timer.

### Scoring and duplicates

| Selected option | Voter | Answer author |
| --- | --- | --- |
| Official correct answer | +2 | — |
| Host lie | −1 | — |
| Another player's answer | 0 | +1 |

Duplicate text stays in separate, separately selectable options with hidden authors during voting. Only the host's **official** correct option awards +2. A player's submission identical to the truth is still their bluff: voting for it awards its author +1, and the voter receives 0. Players cannot vote for their own submission, including a duplicate of the truth. No vote earns 0. Scores can be negative.

## Built-in game library

Every server startup provisions **10 shared quizzes: 8 in Ukrainian and 2 in English, with 10 questions each**. All hosts can launch rooms directly from these games. The library shows language badges and lists packs in the selected interface language first. Quiz text stays in its original language.

The Ukrainian packs cover animals, ocean life, food, plants, space, inventions, geography and everyday curiosities. The English packs are **Nature Has Absolutely No Chill** and **That Cannot Be the Origin**. Each question pairs a surprising correct answer with a plausible false alternative; plausibility is an editorial judgment, so adjust personal copies for your audience.

[content/quizzes.json](content/quizzes.json) is the checked-in catalog. Each question includes a fact-check source URL for maintainers; those annotations are excluded from runtime quiz/player payloads. The catalog is embedded in the executable, so changing it requires rebuilding or restarting `go run .`.

Provisioning uses stable IDs and a transaction to refresh shared catalog entries without duplicates. Missing built-ins are restored on startup. Saved personal quizzes, copies and existing game-session snapshots are preserved. Shared originals cannot be edited through the API; **Make a copy** saves a private, editable version instead. No internet connection is needed to provision or play.

## Storage and access

- SQLite stores quizzes and full session state, including round deadlines, submissions, votes, reveal order and scores. Reconnecting or restarting mid-reveal preserves the remaining hidden results and never awards revealed points twice. Existing completed rounds from the original version retain their scores. Restarting the server with the same database preserves games. An interrupted timed round follows its original deadlines after restart.
- A random HttpOnly, SameSite host cookie identifies the quiz owner. This is designed for private game nights, not account-based hosting. Keep the same browser and cookies to retain host access.
- A random player credential is saved in that browser's local storage. Reloading the page or using **Back to game** restores the same seat. Clearing storage loses that seat. Each phone/browser has its own identity.
- Quiz drafts are saved in local storage; saved quizzes live in SQLite. Editing a quiz does not change existing sessions, which keep a snapshot.
- Player APIs hide correct answers, authors, other submissions and votes until their appropriate phase. Only the host can start or advance questions. Player actions and deadline transitions are serialized before SQLite saves them.
- Use separate browsers or browser profiles to simulate several players on one computer. One browser can host and hold one player seat per room.
- Back up the database using SQLite's backup command or stop the process before copying the database and its WAL files. Do not put the data directory in source control.

For play beyond your home network, run behind an HTTPS reverse proxy and use the HTTPS address for invites. The default server listens on all interfaces at port 8080. No hosted deployment is configured.

## Source

- `main.go`: HTTP API, owner/player authorization, server clock, scoring and SQLite persistence.
- `main_test.go`: timed gameplay and HTTP integration tests.
- `builtin.go`, `builtin_test.go`: transactional catalog provisioning, shared access and preservation tests.
- `content/quizzes.json`: 100 questions and their fact-check references.
- `web/`: responsive bilingual browser UI, translations, host-led reveals and local quiz drafts.
- `scripts/i18n.test.cjs`: language isolation, starter quiz and reveal notification tests.
- `data/varianty.db`: default runtime database (gitignored).

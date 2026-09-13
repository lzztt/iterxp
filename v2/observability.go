package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// StructuredEvent is a single JSONL record in the retained structured log. It is
// intentionally free of prompt bodies, code, patch contents, arbitrary URLs, and
// raw tool output; larger artifacts stay in their existing session files and are
// referenced by path instead of duplicated.
type StructuredEvent struct {
	EventID   string `json:"event_id"`
	Ts        string `json:"ts"`
	Severity  string `json:"severity"`
	Component string `json:"component"`
	Event     string `json:"event"`
	Issue     int    `json:"issue,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Worker    string `json:"worker,omitempty"`
	Version   string `json:"version,omitempty"`
	OpID      string `json:"op_id,omitempty"`

	DurationMS       int64             `json:"duration_ms,omitempty"`
	Model            string            `json:"model,omitempty"`
	Tool             string            `json:"tool,omitempty"`
	Outcome          string            `json:"outcome,omitempty"`
	ErrorClass       string            `json:"error_class,omitempty"`
	Diagnostic       string            `json:"diagnostic,omitempty"`
	ExitCode         *int              `json:"exit_code,omitempty"`
	PromptTokens     *int              `json:"prompt_tokens,omitempty"`
	CompletionTokens *int              `json:"completion_tokens,omitempty"`
	TotalTokens      *int              `json:"total_tokens,omitempty"`
	ContextTokens    int               `json:"context_tokens,omitempty"`
	Retry            bool              `json:"retry,omitempty"`
	Interrupted      bool              `json:"interrupted,omitempty"`
	Accepted         *bool             `json:"accepted,omitempty"`
	Evidence         map[string]string `json:"evidence,omitempty"`
	TraceID          string            `json:"trace_id,omitempty"`
	SpanID           string            `json:"span_id,omitempty"`
}

func newEventID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func obsDir(cfg Config) string {
	if cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(cfg.ConfigDir, "observability")
}

func eventsPath(cfg Config) string {
	return filepath.Join(obsDir(cfg), "events.jsonl")
}

func indexPath(cfg Config) string {
	return filepath.Join(obsDir(cfg), "index.sqlite")
}

func recordEventRaw(cfg Config, ev StructuredEvent) error {
	if cfg.ConfigDir == "" {
		return nil
	}
	if ev.EventID == "" {
		ev.EventID = newEventID()
	}
	if ev.Ts == "" {
		ev.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	path := eventsPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

func openObsDB(path string, readOnly bool) (*sql.DB, error) {
	dsn := "file:" + path
	if readOnly {
		dsn += "?mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func ensureObsSchema(db *sql.DB) error {
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS events (
			event_id TEXT PRIMARY KEY,
			ts TEXT NOT NULL,
			severity TEXT,
			component TEXT,
			event TEXT,
			issue INTEGER,
			attempt INTEGER,
			worker TEXT,
			version TEXT,
			op_id TEXT,
			duration_ms INTEGER,
			model TEXT,
			tool TEXT,
			outcome TEXT,
			error_class TEXT,
			diagnostic TEXT,
			exit_code INTEGER,
			prompt_tokens INTEGER,
			completion_tokens INTEGER,
			total_tokens INTEGER,
			context_tokens INTEGER,
			retry INTEGER,
			interrupted INTEGER,
			accepted INTEGER,
			evidence TEXT,
			trace_id TEXT,
			span_id TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_issue ON events(issue)`,
		`CREATE INDEX IF NOT EXISTS idx_events_component_event ON events(component,event)`,
		`CREATE INDEX IF NOT EXISTS idx_events_op ON events(op_id)`,
		`CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT)`,
	}
	for _, q := range ddl {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func metaGet(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT v FROM meta WHERE k = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func metaSet(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
	return err
}

func importEvents(cfg Config, rebuild bool) (int, error) {
	path := eventsPath(cfg)
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(indexPath(cfg)), 0700); err != nil {
		return 0, err
	}
	db, err := openObsDB(indexPath(cfg), false)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := ensureObsSchema(db); err != nil {
		return 0, err
	}

	var offset int64
	if rebuild {
		if _, err := db.Exec(`DELETE FROM events`); err != nil {
			return 0, err
		}
		if err := metaSet(db, "last_offset", "0"); err != nil {
			return 0, err
		}
	} else {
		v, err := metaGet(db, "last_offset")
		if err != nil {
			return 0, err
		}
		if v != "" {
			offset, _ = strconv.ParseInt(v, 10, 64)
		}
		if offset > fi.Size() {
			offset = 0
			if _, err := db.Exec(`DELETE FROM events`); err != nil {
				return 0, err
			}
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, err
	}
	inserted := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev StructuredEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if err := insertObsEvent(db, ev); err != nil {
			return inserted, err
		}
		inserted++
	}
	if err := metaSet(db, "last_offset", strconv.FormatInt(fi.Size(), 10)); err != nil {
		return inserted, err
	}
	return inserted, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullableInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func ptrIntOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolInt(b bool) any {
	if b {
		return 1
	}
	return 0
}

func insertObsEvent(db *sql.DB, ev StructuredEvent) error {
	evidenceJSON := ""
	if len(ev.Evidence) > 0 {
		if b, err := json.Marshal(ev.Evidence); err == nil {
			evidenceJSON = string(b)
		}
	}
	var accepted any
	if ev.Accepted != nil {
		if *ev.Accepted {
			accepted = 1
		} else {
			accepted = 0
		}
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO events(
		event_id, ts, severity, component, event, issue, attempt, worker, version,
		op_id, duration_ms, model, tool, outcome, error_class, diagnostic,
		exit_code, prompt_tokens, completion_tokens, total_tokens, context_tokens,
		retry, interrupted, accepted, evidence, trace_id, span_id
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ev.EventID, ev.Ts, nullableString(ev.Severity), nullableString(ev.Component), nullableString(ev.Event),
		nullableInt(ev.Issue), nullableInt(ev.Attempt), nullableString(ev.Worker), nullableString(ev.Version),
		nullableString(ev.OpID), nullableInt64(ev.DurationMS), nullableString(ev.Model), nullableString(ev.Tool),
		nullableString(ev.Outcome), nullableString(ev.ErrorClass), nullableString(ev.Diagnostic),
		ptrIntOrNil(ev.ExitCode), ptrIntOrNil(ev.PromptTokens), ptrIntOrNil(ev.CompletionTokens), ptrIntOrNil(ev.TotalTokens),
		nullableInt(ev.ContextTokens), boolInt(ev.Retry), boolInt(ev.Interrupted), accepted,
		nullableString(evidenceJSON), nullableString(ev.TraceID), nullableString(ev.SpanID),
	)
	return err
}

func queryRows(db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[c] = v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type obsQuery struct {
	group   string
	issue   int
	since   string
	until   string
	version string
	model   string
	tool    string
	limit   int
}

func obsFlag(args []string, name string) (string, bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(args[i], name+"=") {
			return strings.TrimPrefix(args[i], name+"="), true
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func parseObsQuery(args []string) obsQuery {
	q := obsQuery{group: "failures"}
	if v, ok := obsFlag(args, "--group"); ok {
		q.group = v
	}
	if v, ok := obsFlag(args, "--issue"); ok {
		q.issue, _ = strconv.Atoi(v)
	}
	q.since, _ = obsFlag(args, "--since")
	q.until, _ = obsFlag(args, "--until")
	q.version, _ = obsFlag(args, "--version")
	q.model, _ = obsFlag(args, "--model")
	q.tool, _ = obsFlag(args, "--tool")
	if v, ok := obsFlag(args, "--limit"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			q.limit = n
		}
	}
	return q
}

func (q obsQuery) where(modelFilter, toolFilter bool) (string, []any) {
	conds := []string{}
	args := []any{}
	add := func(cond string, arg any) {
		conds = append(conds, cond)
		args = append(args, arg)
	}
	if q.issue > 0 {
		add("issue = ?", q.issue)
	}
	if q.version != "" {
		add("version = ?", q.version)
	}
	if q.since != "" {
		add("ts >= ?", q.since)
	}
	if q.until != "" {
		add("ts <= ?", q.until)
	}
	if modelFilter && q.model != "" {
		add("model = ?", q.model)
	}
	if toolFilter && q.tool != "" {
		add("tool = ?", q.tool)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " AND " + strings.Join(conds, " AND "), args
}

func runObsQuery(cfg Config, q obsQuery) ([]byte, error) {
	path := indexPath(cfg)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("index not built: run `iterxp-agent-v2 --obs index` first")
	}
	db, err := openObsDB(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	limit := q.limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	switch q.group {
	case "failures":
		w, args := q.where(false, true)
		rows, err := queryRows(db, `SELECT
			COALESCE(tool,'') AS tool,
			COALESCE(error_class,'') AS error_class,
			SUM(CASE WHEN outcome='timeout' THEN 1 ELSE 0 END) AS timeouts,
			COUNT(*) AS failures,
			MAX(ts) AS last_seen
			FROM events WHERE event='tool_finish' AND (outcome='failure' OR outcome='timeout')`+w+`
			GROUP BY tool, error_class ORDER BY failures DESC, last_seen DESC LIMIT ?`, append(args, limit)...)
		if err != nil {
			return nil, err
		}
		return wrapObsResult(q, rows), nil
	case "time":
		w, args := q.where(false, true)
		rows, err := queryRows(db, `SELECT
			COALESCE(component,'') AS component,
			COUNT(*) AS ops,
			COALESCE(SUM(duration_ms),0) AS total_ms,
			COALESCE(AVG(duration_ms),0) AS avg_ms
			FROM events WHERE duration_ms IS NOT NULL`+w+`
			GROUP BY component ORDER BY total_ms DESC LIMIT ?`, append(args, limit)...)
		if err != nil {
			return nil, err
		}
		return wrapObsResult(q, rows), nil
	case "tokens":
		w, args := q.where(true, false)
		rows, err := queryRows(db, `SELECT
			COALESCE(issue,0) AS issue,
			COALESCE(version,'') AS version,
			COUNT(*) AS calls,
			COALESCE(SUM(prompt_tokens),0) AS prompt_total,
			COALESCE(SUM(completion_tokens),0) AS completion_total,
			COALESCE(SUM(total_tokens),0) AS total_tokens
			FROM events WHERE event='model_finish'`+w+`
			GROUP BY issue, version ORDER BY total_tokens DESC LIMIT ?`, append(args, limit)...)
		if err != nil {
			return nil, err
		}
		return wrapObsResult(q, rows), nil
	case "latency":
		w, args := q.where(true, false)
		rows, err := queryRows(db, `SELECT
			COALESCE(issue,0) AS issue,
			COALESCE(model,'') AS model,
			COUNT(*) AS calls,
			MIN(duration_ms) AS min_ms,
			MAX(duration_ms) AS max_ms,
			AVG(duration_ms) AS avg_ms
			FROM events WHERE event='model_finish' AND duration_ms IS NOT NULL`+w+`
			GROUP BY issue, model ORDER BY calls DESC LIMIT ?`, append(args, limit)...)
		if err != nil {
			return nil, err
		}
		return wrapObsResult(q, rows), nil
	case "current":
		return currentObsResult(cfg, db, limit)
	default:
		return nil, fmt.Errorf("unknown group %q", q.group)
	}
}

func wrapObsResult(q obsQuery, rows []map[string]any) []byte {
	payload := map[string]any{
		"group": q.group,
		"count": len(rows),
		"rows":  rows,
	}
	out, _ := json.MarshalIndent(payload, "", "  ")
	return out
}

func currentObsResult(cfg Config, db *sql.DB, limit int) ([]byte, error) {
	sessions := []map[string]any{}
	entries, err := os.ReadDir(cfg.SessionDir)
	if err == nil {
		nums := []int{}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "issue-") {
				continue
			}
			n, convErr := strconv.Atoi(strings.TrimPrefix(e.Name(), "issue-"))
			if convErr == nil && n > 0 {
				nums = append(nums, n)
			}
		}
		sort.Ints(nums)
		for _, n := range nums {
			s, err := NewSession(Issue{Number: n}, cfg.SessionDir)
			if err != nil {
				continue
			}
			st, err := s.LoadState()
			if err != nil {
				continue
			}
			status := "idle"
			running := st.Worker != nil && st.Worker.Status == "working"
			pending := sessionHasPendingWork(s)
			if running {
				status = "running"
			} else if pending {
				status = "pending"
			}
			row := map[string]any{
				"issue":   n,
				"status":  status,
				"done":    st.Done,
				"version": "",
			}
			if st.Worker != nil {
				row["version"] = st.Worker.Version
			}
			sessions = append(sessions, row)
		}
	}

	inFlight, err := queryRows(db, `SELECT issue, component, event, op_id, ts, tool, model
		FROM events WHERE event IN ('model_start','tool_start') AND op_id IS NOT NULL
		AND op_id NOT IN (SELECT op_id FROM events WHERE event IN ('model_finish','tool_finish'))
		ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"group":     "current",
		"sessions":  sessions,
		"in_flight": inFlight,
	}
	out, _ := json.MarshalIndent(payload, "", "  ")
	return out, nil
}

const obsUsage = `iterxp-agent-v2 --obs <command> [flags]

Commands:
  index       Import new structured events into the SQLite index (incremental).
  rebuild     Delete the index and rebuild it fully from retained events.
  query       Run a read-only report. Default group is "failures".

Query flags:
  --group failures|time|tokens|latency|current
  --issue N
  --since RFC3339
  --until RFC3339
  --version V
  --model M
  --tool T
  --limit N    (default 100, capped at 500)
`

func runObservabilityCLI(cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, obsUsage)
		return 2
	}
	switch args[0] {
	case "index", "rebuild":
		rebuild := args[0] == "rebuild" || hasFlag(args, "--rebuild")
		n, err := importEvents(cfg, rebuild)
		if err != nil {
			fmt.Fprintf(stderr, "observability index failed: %v\n", err)
			_ = recordEventRaw(cfg, StructuredEvent{
				Severity: "error", Component: "indexer", Event: "index_error",
				Outcome: "failure", Tool: "sqlite_index", Diagnostic: err.Error(),
			})
			return 1
		}
		fmt.Fprintf(stdout, "{\"ok\":true,\"indexed\":%d,\"rebuild\":%v,\"index_path\":%q,\"events_path\":%q}\n", n, rebuild, indexPath(cfg), eventsPath(cfg))
		return 0
	case "query":
		q := parseObsQuery(args[1:])
		out, err := runObsQuery(cfg, q)
		if err != nil {
			fmt.Fprintf(stderr, "observability query failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
		return 0
	default:
		fmt.Fprintf(stderr, "observability: unknown command %q\n\n%s", args[0], obsUsage)
		return 2
	}
}

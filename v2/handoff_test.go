package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRecordHandoffPersistsState(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 16, Number: 161}, dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	if err := a.recordHandoff(s, "  root cause: ack bug\nfix: consume after persist  ", " State-Machine-Bug "); err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.HandoffNote != "root cause: ack bug\nfix: consume after persist" {
		t.Fatalf("HandoffNote = %q", st.HandoffNote)
	}
	if st.IssueTypeLabel != "state-machine-bug" {
		t.Fatalf("IssueTypeLabel = %q, want lowercased", st.IssueTypeLabel)
	}
	if st.HandoffPosted || st.IssueLabeled || st.IssueClosed {
		t.Fatalf("completion flags unexpectedly set: %+v", st)
	}
}

func TestCompleteIssuePostsCommentAddsLabelAndCloses(t *testing.T) {
	var mu sync.Mutex
	var gotComment string
	var gotCommentURL string
	var gotLabelURL string
	var gotLabels []string
	var gotCloseURL string
	var gotCloseState string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/161/comments"):
			gotCommentURL = r.URL.Path
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode comment body: %v", err)
			}
			gotComment = body["body"]
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			payload, _ := json.Marshal(map[string]any{"id": 987, "body": body["body"]})
			_, _ = w.Write(payload)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/labels/ack-bug":
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/labels":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"ack-bug","color":"0075ca"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/161/labels"):
			gotLabelURL = r.URL.Path
			var body map[string][]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode label body: %v", err)
			}
			gotLabels = body["labels"]
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"` + gotLabels[0] + `"}]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/issues/161"):
			gotCloseURL = r.URL.Path
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode close body: %v", err)
			}
			gotCloseState = body["state"]
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"closed"}`))
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "token"), 0700)
	os.WriteFile(filepath.Join(home, "token", "github"), []byte("tok\n"), 0600)
	s, err := NewSession(Issue{ID: 16, Number: 161}, dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Repo:          "acme/widgets",
		GitHubAPIBase: srv.URL,
		HomeDir:       home,
		APIBase:       srv.URL,
	}
	client := &Client{cfg: cfg, tokens: map[string]string{"github": "tok"}, http: srv.Client()}
	a := &Agent{cfg: &cfg, client: client}

	if err := a.recordHandoff(s, "trigger: a\nfix: b", "ack-bug"); err != nil {
		t.Fatal(err)
	}
	if err := a.completeIssue(s); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotCommentURL != "/repos/acme/widgets/issues/161/comments" {
		t.Fatalf("comment URL = %q", gotCommentURL)
	}
	if !strings.Contains(gotComment, "## Handoff note") || !strings.Contains(gotComment, "trigger: a") {
		t.Fatalf("comment body = %q", gotComment)
	}
	if gotLabelURL != "/repos/acme/widgets/issues/161/labels" {
		t.Fatalf("label URL = %q", gotLabelURL)
	}
	if len(gotLabels) != 1 || gotLabels[0] != "ack-bug" {
		t.Fatalf("labels = %v", gotLabels)
	}
	if gotCloseURL != "/repos/acme/widgets/issues/161" {
		t.Fatalf("close URL = %q", gotCloseURL)
	}
	if gotCloseState != "closed" {
		t.Fatalf("close state = %q", gotCloseState)
	}

	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.HandoffCommentID != 987 || !st.HandoffPosted || !st.IssueLabeled || !st.IssueClosed {
		t.Fatalf("completion state not persisted: %+v", st)
	}
}

func TestCompleteIssueIsIdempotent(t *testing.T) {
	var mu sync.Mutex
	commentCalls := 0
	labelCalls := 0
	closeCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/comments"):
			commentCalls++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"x"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/labels/ack-bug":
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/labels":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"ack-bug","color":"0075ca"}`))
		case strings.HasSuffix(r.URL.Path, "/issues/161/labels") && r.Method == http.MethodPost:
			labelCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"ack-bug"}]`))
		case strings.HasSuffix(r.URL.Path, "/issues/161") && r.Method == http.MethodPatch:
			closeCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"closed"}`))
		default:
			http.Error(w, "bad", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 16, Number: 161}, dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: "acme/widgets", GitHubAPIBase: srv.URL}
	client := &Client{cfg: cfg, tokens: map[string]string{"github": "tok"}, http: srv.Client()}
	a := &Agent{cfg: &cfg, client: client}

	if err := a.recordHandoff(s, "note", "ack-bug"); err != nil {
		t.Fatal(err)
	}
	if err := a.completeIssue(s); err != nil {
		t.Fatal(err)
	}
	if err := a.completeIssue(s); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if commentCalls != 1 || labelCalls != 1 || closeCalls != 1 {
		t.Fatalf("calls = comment:%d label:%d close:%d, want 1 each", commentCalls, labelCalls, closeCalls)
	}
}

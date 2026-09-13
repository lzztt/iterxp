package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type issuePayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func createIssue(cfg Config, title, body string) error {
	token, err := readToken(cfg.GitHubTokenPath)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(issuePayload{Title: title, Body: body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues", cfg.Repo)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub issue creation returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

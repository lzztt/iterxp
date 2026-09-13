package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// postIssueComment adds a comment to an issue and returns the created comment ID.
func (c *Client) postIssueComment(number int, body string) (int64, error) {
	payload := map[string]string{"body": body}
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", strings.TrimRight(c.cfg.GitHubAPIBase, "/"), c.cfg.Repo, number)
	data, err := c.apiCallJSON("POST", url, payload, nil)
	if err != nil {
		return 0, err
	}
	var comment Comment
	if err := json.Unmarshal(data, &comment); err != nil {
		return 0, err
	}
	return comment.ID, nil
}

// setIssueState closes or reopens an issue.
func (c *Client) setIssueState(number int, state string) error {
	if state != "closed" && state != "open" {
		return fmt.Errorf("invalid issue state: %s", state)
	}
	payload := map[string]string{"state": state}
	url := fmt.Sprintf("%s/repos/%s/issues/%d", strings.TrimRight(c.cfg.GitHubAPIBase, "/"), c.cfg.Repo, number)
	_, err := c.apiCallJSON("PATCH", url, payload, nil)
	return err
}

// listLabels returns the names of labels currently on an issue.
func (c *Client) listLabels(number int) ([]string, error) {
	url := fmt.Sprintf("%s/repos/%s/issues/%d/labels?per_page=100", strings.TrimRight(c.cfg.GitHubAPIBase, "/"), c.cfg.Repo, number)
	data, err := c.apiCallJSON("GET", url, nil, nil)
	if err != nil {
		return nil, err
	}
	var labels []IssueLabel
	if err := json.Unmarshal(data, &labels); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out, nil
}

// addLabel adds a single label to an issue. It is idempotent when the label is already present.
func (c *Client) addLabel(number int, label string) error {
	if strings.TrimSpace(label) == "" {
		return fmt.Errorf("empty label")
	}
	payload := map[string][]string{"labels": {label}}
	url := fmt.Sprintf("%s/repos/%s/issues/%d/labels", strings.TrimRight(c.cfg.GitHubAPIBase, "/"), c.cfg.Repo, number)
	_, err := c.apiCallJSON("POST", url, payload, nil)
	return err
}

// apiCallJSON performs a GitHub API request authenticated with the github token.
// A nil payload means no request body. Response bytes are returned; non-2xx status triggers an error.
func (c *Client) apiCallJSON(method, url string, payload interface{}, headers map[string]string) ([]byte, error) {
	var body bytes.Buffer
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body.Write(data)
	}
	req, err := http.NewRequest(method, url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.tokens["github"])
	req.Header.Set("Accept", "application/vnd.github+json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %s %s status %d: %s", method, url, resp.StatusCode, string(data))
	}
	return data, nil
}

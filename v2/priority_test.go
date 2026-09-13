package main

import "testing"

func TestIssuePriorityFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{name: "urgent", labels: []string{"bug", "urgent"}, want: "urgent"},
		{name: "high", labels: []string{"high"}, want: "high"},
		{name: "medium", labels: []string{"medium"}, want: "medium"},
		{name: "low", labels: []string{"low"}, want: "low"},
		{name: "defaults to low when unlabeled", labels: nil, want: "low"},
		{name: "defaults to low when unrelated labels", labels: []string{"bug", "feature"}, want: "low"},
		{name: "case insensitive", labels: []string{"URGENT"}, want: "urgent"},
		{name: "first recognized wins", labels: []string{"high", "urgent"}, want: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := Issue{}
			for _, l := range tt.labels {
				issue.Labels = append(issue.Labels, IssueLabel{Name: l})
			}
			if got := issuePriority(issue); got != tt.want {
				t.Fatalf("issuePriority() = %q, want %q", got, tt.want)
			}
		})
	}
}

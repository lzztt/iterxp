package main

import "strings"

// issuePriority returns the first recognized priority label (urgent, high,
// medium, low) on an issue. Unlabeled or otherwise-labeled issues default to
// low priority.
func issuePriority(issue Issue) string {
	for _, label := range issue.Labels {
		name := strings.ToLower(strings.TrimSpace(label.Name))
		switch name {
		case "urgent", "high", "medium", "low":
			return name
		}
	}
	return "low"
}

package review

import "strings"

// BuildAddressedReply formats the reply we post when an item is
// addressed: "Addressed in <sha7>: <subject80>" — with the subject
// truncated to 80 chars and omitted entirely if empty.
//
// Including the subject keeps the reply useful even if the SHA link
// later 404s after a rebase/squash (typical in stacked PR workflows).
func BuildAddressedReply(sha, subject string) string {
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "Addressed in " + short
	}
	if len(subject) > 80 {
		subject = subject[:80]
	}
	return "Addressed in " + short + ": " + subject
}

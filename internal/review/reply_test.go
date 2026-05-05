package review

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildAddressedReply(t *testing.T) {
	tests := []struct {
		name    string
		sha     string
		subject string
		want    string
	}{
		{
			name:    "HappyPathBothSHAAndSubject",
			sha:     "abc1234def5678",
			subject: "fix the thing",
			want:    "Addressed in abc1234: fix the thing",
		},
		{
			name:    "EmptySubjectFallsBackToBareForm",
			sha:     "abc1234def5678",
			subject: "",
			want:    "Addressed in abc1234",
		},
		{
			name:    "SubjectLongerThan80CharsTruncates",
			sha:     "abc1234def5678",
			subject: "this is a very long subject line that exceeds the eighty character limit for subjects in replies",
			want:    "Addressed in abc1234: this is a very long subject line that exceeds the eighty character limit for sub",
		},
		{
			name:    "SHAShorterThan7StaysAsIs",
			sha:     "abc12",
			subject: "fix",
			want:    "Addressed in abc12: fix",
		},
		{
			name:    "WhitespaceOnlySubjectTreatedAsEmpty",
			sha:     "abc1234def5678",
			subject: "   \t  ",
			want:    "Addressed in abc1234",
		},
		{
			name:    "ExactlySevenCharSHA",
			sha:     "abcdef0",
			subject: "subject",
			want:    "Addressed in abcdef0: subject",
		},
		{
			name:    "SubjectExactly80Chars",
			sha:     "abc1234def5678",
			subject: "12345678901234567890123456789012345678901234567890123456789012345678901234567890",
			want:    "Addressed in abc1234: 12345678901234567890123456789012345678901234567890123456789012345678901234567890",
		},
		{
			name:    "SubjectExactly81CharsTruncates",
			sha:     "abc1234def5678",
			subject: "123456789012345678901234567890123456789012345678901234567890123456789012345678901",
			want:    "Addressed in abc1234: 12345678901234567890123456789012345678901234567890123456789012345678901234567890",
		},
		{
			name:    "SubjectWithLeadingTrailingWhitespaceTrimmed",
			sha:     "abc1234def5678",
			subject: "  trimmed subject  ",
			want:    "Addressed in abc1234: trimmed subject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAddressedReply(tt.sha, tt.subject)
			assert.Equal(t, tt.want, got)
		})
	}
}

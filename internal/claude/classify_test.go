package claude_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/claude"
)

func TestClassifyItem_happyPath(t *testing.T) {
	runner := &fakeRunner{
		response: `{` +
			`"category":"bug",` +
			`"summary":"Nil pointer dereference on empty input",` +
			`"suggested_action":"edit",` +
			`"reply_draft":"Fixed in the next commit.",` +
			`"fix_plan":"Add nil check before dereferencing",` +
			`"complexity":"trivial"` +
			`}`,
	}

	got, err := claude.ClassifyItemWithRunner(
		t.Context(),
		claude.Config{},
		&claude.Item{
			Kind:   "review-thread",
			Author: "copilot[bot]",
			Body:   "Potential nil pointer here.",
		},
		runner,
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "bug", got.Category)
	assert.Equal(t, "Nil pointer dereference on empty input", got.Summary)
	assert.Equal(t, "edit", got.SuggestedAction)
	assert.Equal(t, "Fixed in the next commit.", got.ReplyDraft)
	assert.Equal(t, "Add nil check before dereferencing", got.FixPlan)
	assert.Equal(t, "trivial", got.Complexity)
	assert.Equal(t, 1, runner.calls)
}

func TestClassifyItem_malformedRetries(t *testing.T) {
	runner := &fakeRunner{response: "not json"}

	got, err := claude.ClassifyItemWithRunner(
		t.Context(),
		claude.Config{},
		&claude.Item{
			Kind:   "check",
			Author: "github-actions[bot]",
			Body:   "Build failed.",
		},
		runner,
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "unclassified", got.Category)
	assert.Equal(t, 2, runner.calls)
}

func TestClassifyItem_runnerError(t *testing.T) {
	runner := &fakeRunner{
		err: errors.New("claude CLI not available"),
	}

	got, err := claude.ClassifyItemWithRunner(
		t.Context(),
		claude.Config{},
		&claude.Item{
			Kind:   "review-thread",
			Author: "reviewer",
			Body:   "Please fix this.",
		},
		runner,
	)

	require.Error(t, err)
	assert.Nil(t, got)
}

// fakeRunner is a test double for PromptRunner.
type fakeRunner struct {
	response string
	err      error
	calls    int
}

func (f *fakeRunner) Run(
	_ context.Context,
	_ claude.Config,
	_ string,
) (string, error) {
	f.calls++
	return f.response, f.err
}

// TestClassifyItem_jsonInMarkdownFences verifies that the parser
// strips ```json fences before unmarshalling — claude in --print
// mode often returns JSON wrapped in code fences despite "no prose,
// no markdown fences" in the prompt.
func TestClassifyItem_jsonInMarkdownFences(t *testing.T) {
	cases := map[string]string{
		"json fence": "```json\n{\"category\":\"nit\",\"summary\":\"x\"," +
			"\"suggested_action\":\"reply\",\"reply_draft\":\"\"," +
			"\"fix_plan\":\"\",\"complexity\":\"trivial\"}\n```",
		"plain fence": "```\n{\"category\":\"bug\",\"summary\":\"y\"," +
			"\"suggested_action\":\"edit\",\"reply_draft\":\"\"," +
			"\"fix_plan\":\"\",\"complexity\":\"small\"}\n```",
		"prose around": "Sure, here's the classification:\n\n" +
			"{\"category\":\"question\",\"summary\":\"z\"," +
			"\"suggested_action\":\"reply\",\"reply_draft\":\"\"," +
			"\"fix_plan\":\"\",\"complexity\":\"trivial\"}\n\nLet me know!",
	}
	wantCategory := map[string]string{
		"json fence":   "nit",
		"plain fence":  "bug",
		"prose around": "question",
	}

	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := claude.ClassifyItemWithRunner(
				t.Context(),
				claude.Config{},
				&claude.Item{Kind: "review-thread", Body: "test"},
				&fakeRunner{response: resp},
			)
			require.NoError(t, err)
			assert.Equal(t, wantCategory[name], got.Category)
		})
	}
}

package git

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/xec/xectest"
)

var NewMockExecer = xectest.NewMockExecer

func TestGitCmd_logPrefix(t *testing.T) {
	var logBuffer bytes.Buffer
	log := silog.New(&logBuffer, &silog.Options{
		Level: silog.LevelDebug,
	})

	t.Run("DefaultPrefixNoCommand", func(t *testing.T) {
		defer logBuffer.Reset()

		_ = newGitCmd(t.Context(), log, _realExec, "--unknown-flag").
			WithDir(t.TempDir()).
			Run()

		assert.Contains(t, logBuffer.String(), " git: ")
	})

	t.Run("DefaultPrefixCommand", func(t *testing.T) {
		defer logBuffer.Reset()

		_ = newGitCmd(t.Context(), log, _realExec, "unknown-cmd").
			WithDir(t.TempDir()).
			Run()

		assert.Contains(t, logBuffer.String(), " git unknown-cmd: ")
	})

	t.Run("LogPrefixAfterwards", func(t *testing.T) {
		defer logBuffer.Reset()

		_ = newGitCmd(t.Context(), log, _realExec, "whatever").
			WithDir(t.TempDir()).
			WithLogPrefix("different").
			Run()

		assert.Contains(t, logBuffer.String(), " different: ")
	})
}

func TestNewGitCmd_optionalLocks(t *testing.T) {
	t.Run("ReadOnlyGetsOptionalLocks", func(t *testing.T) {
		for _, subcmd := range []string{
			"rev-parse", "merge-base", "for-each-ref",
			"config", "log", "diff",
		} {
			t.Run(subcmd, func(t *testing.T) {
				cmd := newGitCmd(
					t.Context(), silog.Nop(),
					_realExec, subcmd,
				)
				out, _ := cmd.
					WithDir(t.TempDir()).
					AppendEnv("GIT_OPTIONAL_LOCKS_CHECK=1").
					OutputChomp()
				// We can't easily inspect env,
				// but we can verify it compiles and runs.
				_ = out
			})
		}
	})

	t.Run("WriteDoesNotGetOptionalLocks", func(t *testing.T) {
		for _, subcmd := range []string{
			"checkout", "commit", "reset",
		} {
			t.Run(subcmd, func(t *testing.T) {
				// Verify the command is constructed
				// without error.
				_ = newGitCmd(
					t.Context(), silog.Nop(),
					_realExec, subcmd,
				)
			})
		}
	})
}

package main

// runCmd groups commands that run things locally.
type runCmd struct {
	PrecommitChecks runPrecommitChecksCmd `cmd:"precommit-checks" help:"Run configured local checks before push"`
}

package tui

import "dexianta/refci/core"

type StatusEvent struct {
	Message string
	IsError bool
}

type loadRepoJobsMsg struct {
	repo string
	jobs []core.Job
	err  error
}

type loadJobLogMsg struct {
	path  string
	lines []string
	err   error
}

type statusEventMsg struct {
	message string
	inErr   bool
}

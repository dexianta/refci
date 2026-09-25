package tui

import "dexianta/refci/core"

type StatusEvent struct {
	Message string
	IsError bool
}

// JobRequest asks a worker to restart or cancel one job run.
type JobRequest struct {
	RunID  string
	Repo   string
	Name   string
	Branch string
	SHA    string
}

type loadRepoJobsMsg struct {
	repo     string
	jobs     []core.Job
	jobNames []string
	err      error
}

type loadRepoListMsg struct {
	repos []string
	err   error
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

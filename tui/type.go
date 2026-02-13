package tui

import "dexianta/refci/core"

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

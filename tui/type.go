package tui

import "dexianta/nci/core"

type addRepoMsg struct {
	repo core.CodeRepo
	err  error
}

type loadRepoMsg struct {
	repos []core.CodeRepo
	err   error
}

type deleteRepoMsg struct {
	err error
}

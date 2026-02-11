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
	deleted string
	repos   []core.CodeRepo
	err     error
}

type addBranchMsg struct {
	added core.BranchConf
	err   error
}

type delBranchMsg struct {
	del core.BranchConf
	err error
}

type branchesLoadBranchConfMsg struct {
	branchConf []core.BranchConf
	err        error
}

type branchesLoadJobMsg struct {
	jobs []core.Job
	err  error
}

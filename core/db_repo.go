package core

import "time"

type CodeRepo struct {
	Repo string
	URL  string // should we distinguish ssh or https
}

type RepoSetting struct {
	Repo  string
	Key   string
	Value string
}

type GlobalSetting struct {
	LogRetentionDays int
}

type Job struct {
	Repo   string
	Name   string
	Branch string
	SHA    string
	Start  time.Time
	End    time.Time
	Status string
	Msg    string
}

var (
	StatusRunning  = "running"
	StatusPending  = "pending"
	StatusCanceled = "canceled"
	StatusFailed   = "failed"
	StatusFinished = "finished"
)

type JobFilter struct {
	Repo   string
	Name   string
	Branch string
	Status string
}

type DbRepo interface {
	LatestJobByNameBranch(repo, name, branch string) (Job, error)
	CreateJob(repo, name, branch, sha string) error
	UpdateJob(repo, name, branch, sha, status, msg string) error // for cancel, or finish etc
	ListJob(filter JobFilter) ([]Job, error)
}

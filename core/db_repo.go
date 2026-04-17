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
	RunID        string
	Repo         string
	Name         string
	Branch       string
	SHA          string
	CommitAuthor string
	LogPath      string
	Start        time.Time
	End          time.Time
	Status       string
	Msg          string
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
	Limit  int
}

type DbRepo interface {
	LatestJobByNameBranch(repo, name, branch string) (Job, error)
	JobByRunID(runID string) (Job, error)
	CreateJob(runID, repo, name, branch, sha, commitAuthor string) error
	UpdateJob(runID, status, msg, logPath string) error // for cancel, or finish etc
	ListJob(filter JobFilter) ([]Job, error)
	ListJobNames(repo string) ([]string, error)
}

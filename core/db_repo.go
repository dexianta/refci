package core

import "time"

type CodeRepo struct {
	Repo string
	URL  string // should we distinguish ssh or https
}

type BranchConf struct {
	Repo       string
	RefPattern string
	ScriptPath string
}

type Job struct {
	Repo   string
	Ref    string // refs/heads/main
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
	Ref    string
	Status string
}

type DbRepo interface {
	SaveBranchConf(bc BranchConf) error
	ListBranchConf(repo string) ([]BranchConf, error)
	UpdateBranchConf(bc BranchConf) error
	DeleteBranchConf(repo, refPattern string) error
	DeleteBranchConfByRepo(repo string) error

	SaveCodeRepo(repo CodeRepo) error
	ListCodeRepo() ([]CodeRepo, error)
	DeleteRepo(repo string) error

	FetchJob(repo, ref, sha string) (Job, error)
	CreateJob(repo, ref, sha string) error
	UpdateJob(repo, ref, sha, status, msg string) error // for cancel, or finish etc
	ListJob(filter JobFilter) ([]Job, error)
}

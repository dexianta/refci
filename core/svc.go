package core

import (
	"context"
	"errors"
	"path/filepath"
)

type SvcImpl struct {
	dbRepo DbRepo
}


func NewSvcImpl(dbRepo DbRepo) SvcImpl {
	return SvcImpl{dbRepo: dbRepo}
}

func (s SvcImpl) CloneRepo(repo, url string) error {
	if repo == "" {
		return errors.New("repo is empty")
	}
	// use clone helper
	err := CloneMirror(
		context.Background(),
		url,
		filepath.Join(Root, "repos", ToLocalRepo(repo)),
	) // just use convention, to get current path + "/repos")
	if err != nil {
		return err
	}

	return s.dbRepo.SaveCodeRepo(CodeRepo{
		Repo: repo,
		URL:  url,
	})
}

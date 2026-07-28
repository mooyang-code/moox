package rpc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type stagedFactorArtifact struct {
	original string
	staged   string
}

type factorArtifactStage struct {
	artifacts []stagedFactorArtifact
}

func stageFactorArtifacts(factorsDir, name string) (*factorArtifactStage, error) {
	stage := &factorArtifactStage{}
	paths := []string{
		filepath.Join(factorsDir, name+".py"),
		filepath.Join(factorsDir, ".versions", "factor", name),
	}
	for _, path := range paths {
		artifact, exists, err := stageFactorArtifact(path, name)
		if err != nil {
			return nil, errors.Join(err, stage.Restore())
		}
		if exists {
			stage.artifacts = append(stage.artifacts, artifact)
		}
	}
	return stage, nil
}

func stageFactorArtifact(path, name string) (stagedFactorArtifact, bool, error) {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stagedFactorArtifact{}, false, nil
		}
		return stagedFactorArtifact{}, false, err
	}
	if !info.IsDir() {
		return stagedFactorArtifact{}, false, fmt.Errorf("factor artifact parent %s is not a directory", parent)
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stagedFactorArtifact{}, false, nil
		}
		return stagedFactorArtifact{}, false, err
	}
	tmp, err := os.CreateTemp(parent, ".delete-"+name+"-*")
	if err != nil {
		return stagedFactorArtifact{}, false, err
	}
	staged := tmp.Name()
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(staged)
		return stagedFactorArtifact{}, false, closeErr
	}
	if err := os.Remove(staged); err != nil {
		return stagedFactorArtifact{}, false, err
	}
	if err := os.Rename(path, staged); err != nil {
		return stagedFactorArtifact{}, false, err
	}
	return stagedFactorArtifact{original: path, staged: staged}, true, nil
}

func (s *factorArtifactStage) Restore() error {
	if s == nil {
		return nil
	}
	var errs []error
	for i := len(s.artifacts) - 1; i >= 0; i-- {
		artifact := s.artifacts[i]
		if err := os.Rename(artifact.staged, artifact.original); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("restore factor artifact %s: %w", artifact.original, err))
		}
	}
	return errors.Join(errs...)
}

func (s *factorArtifactStage) Remove() error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, artifact := range s.artifacts {
		if err := os.RemoveAll(artifact.staged); err != nil {
			errs = append(errs, fmt.Errorf("remove staged factor artifact %s: %w", artifact.staged, err))
		}
	}
	return errors.Join(errs...)
}

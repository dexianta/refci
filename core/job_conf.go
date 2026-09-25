package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseJobConfs parses .refci/conf.yml, a top-level map of job name to job:
//
//	my-job:
//	  branch_pattern: main
//	  path_patterns:
//	    - services/**
//	  script: .refci/main.sh
func ParseJobConfs(raw string) ([]JobConf, error) {
	var file map[string]JobConf
	if err := yaml.Unmarshal([]byte(raw), &file); err != nil {
		return nil, fmt.Errorf("parse .refci/conf.yml: %w", err)
	}

	out := make([]JobConf, 0, len(file))
	for _, name := range slices.Sorted(maps.Keys(file)) {
		conf := file[name]
		conf.Name = strings.TrimSpace(name)
		if conf.Name == "" {
			return nil, fmt.Errorf("job name must not be empty")
		}
		if strings.TrimSpace(conf.ScriptPath) == "" {
			return nil, fmt.Errorf("job %q: script is required", conf.Name)
		}
		pattern := normalizeBranchPattern(conf.BranchPattern)
		if strings.Contains(strings.TrimSuffix(pattern, "*"), "*") {
			return nil, fmt.Errorf("job %q: only a trailing wildcard is supported in branch_pattern %q", conf.Name, conf.BranchPattern)
		}
		out = append(out, conf)
	}
	return out, nil
}

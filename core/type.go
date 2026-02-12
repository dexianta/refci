package core

type JobConf struct {
	Repo          string   `yaml:"-"`
	Name          string   `yaml:"-"`
	BranchPattern string   `yaml:"branch_pattern"`
	PathPatterns  []string `yaml:"path_patterns"`
	ScriptPath    string   `yaml:"script"`
}

package core

import (
	"strings"
	"testing"
)

func TestParseJobConfs(t *testing.T) {
	confs, err := ParseJobConfs("b:\n  script: b.sh\na:\n  branch_pattern: feature-*\n  script: a.sh\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(confs) != 2 || confs[0].Name != "a" || confs[0].ScriptPath != "a.sh" || confs[1].Name != "b" {
		t.Fatalf("unexpected confs: %+v", confs)
	}

	for raw, want := range map[string]string{
		"a: [":                         "parse .refci/conf.yml",
		"a:\n  branch_pattern: main\n": "script is required",
		"a:\n  branch_pattern: f*x\n  script: a.sh": "trailing wildcard",
	} {
		if _, err := ParseJobConfs(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseJobConfs(%q) error = %v, want %q", raw, err, want)
		}
	}
}

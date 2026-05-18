package main

import (
	"strings"
	"testing"
)

func TestUpsertRefciSSHHostBlockAddsManagedHost(t *testing.T) {
	got, err := upsertRefciSSHHostBlock("", "refci-owner--repo", "/tmp/refci-owner-repo")
	if err != nil {
		t.Fatalf("upsert returned error: %v", err)
	}

	wantParts := []string{
		"# refci:begin refci-owner--repo",
		"Host refci-owner--repo",
		"HostName github.com",
		"User git",
		"IdentityFile /tmp/refci-owner-repo",
		"IdentitiesOnly yes",
		"# refci:end refci-owner--repo",
	}
	for _, part := range wantParts {
		if !strings.Contains(got, part) {
			t.Fatalf("config missing %q:\n%s", part, got)
		}
	}
}

func TestUpsertRefciSSHHostBlockReplacesManagedHost(t *testing.T) {
	config := refciSSHHostBlock("refci-owner--repo", "/tmp/old-key")

	got, err := upsertRefciSSHHostBlock(config, "refci-owner--repo", "/tmp/new-key")
	if err != nil {
		t.Fatalf("upsert returned error: %v", err)
	}
	if strings.Contains(got, "/tmp/old-key") {
		t.Fatalf("old key was not replaced:\n%s", got)
	}
	if !strings.Contains(got, "IdentityFile /tmp/new-key") {
		t.Fatalf("new key missing:\n%s", got)
	}
}

func TestUpsertRefciSSHHostBlockRejectsUnmanagedHost(t *testing.T) {
	config := "Host refci-owner--repo\n  HostName github.com\n"

	_, err := upsertRefciSSHHostBlock(config, "refci-owner--repo", "/tmp/refci-owner-repo")
	if err == nil {
		t.Fatal("expected unmanaged host conflict")
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSkillUsesBootstrapInstaller(t *testing.T) {
	if got := installerNameForArgs([]string{"--ai-skill"}); got != "bootstrap-install.sh" {
		t.Fatalf("installer for --ai-skill = %q", got)
	}
	if got := installerNameForArgs([]string{"--mode", "full"}); got != platformInstaller() {
		t.Fatalf("installer for normal install = %q, want %q", got, platformInstaller())
	}
}

func TestUpgradeCandidatesUseInstalledBootstrapFirst(t *testing.T) {
	candidates := upgradeInstallerCandidates()
	if len(candidates) != 3 {
		t.Fatalf("upgrade candidates = %v", candidates)
	}
	want := []string{
		filepath.Join("/usr/local/libexec/webport/installer/scripts", "bootstrap-install.sh"),
		filepath.Join("scripts", "bootstrap-install.sh"),
		filepath.Join(filepath.Dir(os.Args[0]), "scripts", "bootstrap-install.sh"),
	}
	for i := range want {
		if candidates[i] != want[i] {
			t.Fatalf("upgrade candidates = %v, want %v", candidates, want)
		}
	}
}

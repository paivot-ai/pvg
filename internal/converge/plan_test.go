package converge

import (
	"path/filepath"
	"testing"
)

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"pvg 1.55.0", "1.55.0"},
		{"nd version v0.10.20", "0.10.20"},
		{"vlt v0.11.0", "0.11.0"},
		{"vlt 0.11.0", "0.11.0"},
		{"v1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3"},
		{"pvg dev-abc1234 (2026-04-06T08:34:56Z)", ""},
		{"pvg dev", ""},
		{"", ""},
		{"nd version v0.10.20\nextra line", "0.10.20"},
	}
	for _, tt := range tests {
		if got := normalizeVersion(tt.in); got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSameVersion(t *testing.T) {
	tests := []struct {
		installed, pinned string
		want              bool
	}{
		{"pvg 1.55.0", "v1.55.0", true},
		{"pvg 1.55.0", "1.55.0", true},
		{"nd version v0.10.20", "v0.10.20", true},
		{"vlt v0.11.0", "v0.11.0", true},
		{"pvg 1.54.0", "v1.55.0", false},
		{"pvg dev-abc1234", "v1.55.0", false},
		{"", "v1.55.0", false},
		{"pvg 1.55.0", "", false},
	}
	for _, tt := range tests {
		if got := sameVersion(tt.installed, tt.pinned); got != tt.want {
			t.Errorf("sameVersion(%q, %q) = %v, want %v", tt.installed, tt.pinned, got, tt.want)
		}
	}
}

func TestSelectInstallDir(t *testing.T) {
	home := "/home/u"
	probeOf := func(writableDirs map[string]bool, sudo bool) dirProbe {
		return dirProbe{
			writable: func(dir string) bool { return writableDirs[dir] },
			sudoOK:   func() bool { return sudo },
			homeDir:  func() (string, error) { return home, nil },
		}
	}

	tests := []struct {
		name          string
		installedPath string
		writable      map[string]bool
		sudo          bool
		wantDir       string
		wantSudo      bool
		wantFresh     bool
		wantErr       bool
	}{
		{
			name:          "installed in writable dir replaces in place",
			installedPath: "/home/u/go/bin/nd",
			writable:      map[string]bool{"/home/u/go/bin": true},
			wantDir:       "/home/u/go/bin",
		},
		{
			name:          "installed in root-owned dir uses sudo in place",
			installedPath: "/usr/local/bin/nd",
			writable:      map[string]bool{},
			sudo:          true,
			wantDir:       "/usr/local/bin",
			wantSudo:      true,
		},
		{
			name:          "installed in root-owned dir without sudo fails",
			installedPath: "/usr/local/bin/nd",
			writable:      map[string]bool{},
			sudo:          false,
			wantErr:       true,
		},
		{
			name:      "fresh install prefers writable /usr/local/bin",
			writable:  map[string]bool{"/usr/local/bin": true},
			wantDir:   "/usr/local/bin",
			wantFresh: true,
		},
		{
			name:      "fresh install uses sudo for /usr/local/bin",
			writable:  map[string]bool{},
			sudo:      true,
			wantDir:   "/usr/local/bin",
			wantSudo:  true,
			wantFresh: true,
		},
		{
			name:      "fresh install falls back to ~/.local/bin",
			writable:  map[string]bool{},
			sudo:      false,
			wantDir:   filepath.Join(home, ".local", "bin"),
			wantFresh: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectInstallDir(tt.installedPath, probeOf(tt.writable, tt.sudo))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectInstallDir() error: %v", err)
			}
			if got.Dir != tt.wantDir || got.Sudo != tt.wantSudo || got.Fresh != tt.wantFresh {
				t.Fatalf("selectInstallDir() = %+v, want dir=%s sudo=%v fresh=%v",
					got, tt.wantDir, tt.wantSudo, tt.wantFresh)
			}
		})
	}
}

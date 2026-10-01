package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Folder is one place the app can keep its config, keys and known_hosts.
type Folder struct {
	Path   string `json:"path"`
	Label  string `json:"label"`
	Exists bool   `json:"exists"`
	Active bool   `json:"active"`
}

type settings struct {
	SSHDir   string `json:"sshDir,omitempty"`
	Accepted string `json:"accepted,omitempty"` // version of the first-run notice the user accepted
}

// dataDir holds the app's own settings.
func dataDir() string {
	if d := os.Getenv("SSHKEYS_DATA_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("SSHKEYS_SSH_DIR"); d != "" {
		return filepath.Join(d, ".sshkeys-app") // test runs never touch the real settings
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "SSHHostManager")
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "SSHHostManager")
	}
	return filepath.Join(homeDir(), ".sshhostmanager")
}

func settingsPath() string { return filepath.Join(dataDir(), "settings.json") }

func loadSettings() settings {
	var s settings
	if data, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

func saveSettings(s settings) error {
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(settingsPath(), data, 0o600)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(expandHome(a)), filepath.Clean(expandHome(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// oneDriveRoots lists the signed-in user's OneDrive folders.
func oneDriveRoots(dev bool) []string {
	if dev {
		if d := os.Getenv("SSHKEYS_ONEDRIVE_DIR"); d != "" {
			return []string{d}
		}
		return nil
	}
	var out []string
	for _, k := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
		if d := os.Getenv(k); d != "" && isDir(d) {
			out = append(out, d)
		}
	}
	return out
}

// exeSSHDir is the app's own folder when the app was put inside a .ssh folder.
func exeSSHDir(dev bool) string {
	if dev {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if d := filepath.Dir(exe); strings.EqualFold(filepath.Base(d), ".ssh") {
		return d
	}
	return ""
}

// chooseDir picks the folder to work in at startup: the one the user chose,
// else a .ssh folder the app was placed in, else OneDrive's .ssh when there is
// one, else ~/.ssh.
func (t *Tools) chooseDir() string {
	if s := loadSettings().SSHDir; s != "" && os.MkdirAll(s, 0o700) == nil {
		return s
	}
	if d := exeSSHDir(t.Dev); d != "" && !samePath(d, t.Default) {
		return d
	}
	for _, r := range oneDriveRoots(t.Dev) {
		if p := filepath.Join(r, ".ssh"); isDir(p) {
			return p
		}
	}
	return t.Default
}

// folders is the list offered in the folder dropdown.
func (t *Tools) folders() []Folder {
	var out []Folder
	add := func(p, label string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		for _, f := range out {
			if samePath(f.Path, p) {
				return
			}
		}
		out = append(out, Folder{Path: p, Label: label, Exists: isDir(p), Active: samePath(p, t.Dir)})
	}
	for _, r := range oneDriveRoots(t.Dev) {
		add(filepath.Join(r, ".ssh"), "OneDrive")
	}
	add(t.Default, "This PC")
	add(exeSSHDir(t.Dev), "App folder")
	add(loadSettings().SSHDir, "Custom")
	add(t.Dir, "Custom")
	return out
}

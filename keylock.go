package main

// Keys that are protected by a passphrase.
//
// The app never sees, asks for, or stores a passphrase. It only asks
// ssh-keygen whether a key file has one, asks Windows' ssh-agent which keys it
// is holding, and opens a terminal window in which ssh-add or ssh-keygen talk
// to the user directly.
//
// "Unlocked" means the key is held by the OpenSSH Authentication Agent, a
// Windows service. That service, not this app, keeps the unlocked key (it
// stores it protected for the Windows account) until it is removed again.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Key lock states shown in the window.
const (
	lockNone     = "none"     // the key file has no passphrase
	lockLocked   = "locked"   // it has one and the agent isn't holding the key
	lockUnlocked = "unlocked" // it has one and the agent is holding the key
)

type passEntry struct {
	stamp int64
	has   bool
}

var (
	passMu    sync.Mutex
	passCache = map[string]passEntry{}
)

// hasPassphrase reports whether a private key file is protected by a
// passphrase, by asking ssh-keygen to read it with an empty one.
func (t *Tools) hasPassphrase(key string) bool {
	st := fileStamp(key)
	passMu.Lock()
	e, ok := passCache[key]
	passMu.Unlock()
	if ok && e.stamp == st {
		return e.has
	}
	r, err := run(10*time.Second, t.Keygen, "-y", "-P", "", "-f", key)
	has := err == nil && r.Code != 0 && strings.Contains(strings.ToLower(r.Out), "passphrase")
	passMu.Lock()
	passCache[key] = passEntry{st, has}
	passMu.Unlock()
	return has
}

// agentKeys lists the fingerprints of the keys the agent is holding. The
// second result is false when the agent isn't running.
func (t *Tools) agentKeys() (map[string]bool, bool) {
	if t.Add == "" {
		return nil, false
	}
	r, err := run(8*time.Second, t.Add, "-l")
	if err != nil || r.Code == 2 || strings.Contains(r.Out, "Error connecting to agent") {
		return nil, false
	}
	fps := map[string]bool{}
	for _, l := range strings.Split(r.Out, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && strings.HasPrefix(f[1], "SHA256:") {
			fps[f[1]] = true
		}
	}
	return fps, true
}

// pubFingerprint is the SHA256 fingerprint of a public key file.
func pubFingerprint(pubPath string) string {
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return ""
	}
	if p := strings.Fields(string(data)); len(p) >= 2 {
		return fingerprint(p[1])
	}
	return ""
}

// keyFor returns the private key path of a server.
func (a *App) keyFor(alias string) (string, error) {
	h, err := a.findHost(alias)
	if err != nil {
		return "", err
	}
	if h.IdentityFile == "" {
		return "", errors.New("this server has no key file set")
	}
	return filepath.Clean(expandHome(h.IdentityFile)), nil
}

// unlock opens a terminal in which ssh-add asks for the passphrase and hands
// the key to the agent. It returns when that window has closed.
func (a *App) unlock(alias string) error {
	key, err := a.keyFor(alias)
	if err != nil {
		return err
	}
	return runConsole("Unlock the key for "+alias, a.t.Add, key)
}

// lockKey takes a key out of the agent. No passphrase is needed for that.
func (t *Tools) lockKey(key string) error {
	r, err := run(10*time.Second, t.Add, "-d", key)
	if err != nil {
		return err
	}
	if r.Code != 0 {
		return fmt.Errorf("could not lock the key: %s", lastLines(r.Out, 2))
	}
	return nil
}

func (a *App) lock(alias string) error {
	key, err := a.keyFor(alias)
	if err != nil {
		return err
	}
	return a.t.lockKey(key)
}

// changePassphrase opens a terminal in which ssh-keygen asks for the current
// passphrase, if there is one, and the new one. An empty new one removes it.
func (a *App) changePassphrase(alias string) error {
	key, err := a.keyFor(alias)
	if err != nil {
		return err
	}
	return runConsole("Passphrase for the key of "+alias, a.t.Keygen, "-p", "-f", key)
}

// startAgent switches the OpenSSH Authentication Agent service on, through
// one administrator prompt, and waits for it to answer.
func (a *App) startAgent() error {
	if err := elevate("powershell.exe", `-NoProfile -WindowStyle Hidden -Command "Set-Service ssh-agent -StartupType Automatic; Start-Service ssh-agent"`); err != nil {
		return err
	}
	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		if _, on := a.t.agentKeys(); on {
			return nil
		}
	}
	return errors.New("the key agent did not start; it may have been declined at the administrator prompt")
}

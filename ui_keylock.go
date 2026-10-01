package main

// Buttons for keys that have a passphrase: Unlock, Lock, Passphrase, and what
// happens to unlocked keys when the app closes. See keylock.go for what
// "unlocked" means; the passphrase is only ever typed into ssh's own terminal.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	untilClose = "close" // lock the key again when the app closes
	untilKeep  = "keep"  // leave it unlocked until the user locks it
)

// saveSession records which keys to lock at close, so a crash can't leave
// them unlocked for good. Call with g.mu held.
func (g *gui) saveSession() {
	s := loadSettings()
	s.SessionKeys = s.SessionKeys[:0]
	for k := range g.sessionKeys {
		s.SessionKeys = append(s.SessionKeys, k)
	}
	sort.Strings(s.SessionKeys)
	g.try(saveSettings(s))
}

// lockLeftovers locks keys that were meant to be locked when the app closed
// but weren't, because the app or the computer stopped unexpectedly.
func (g *gui) lockLeftovers() {
	s := loadSettings()
	if len(s.SessionKeys) == 0 {
		return
	}
	held, on := g.app.t.agentKeys()
	n := 0
	for _, k := range s.SessionKeys {
		if on && held[pubFingerprint(k+".pub")] && g.app.t.lockKey(k) == nil {
			n++
		}
	}
	s.SessionKeys = nil
	saveSettings(s)
	if n > 0 {
		g.reload("", false)
		g.notify("Locked "+plural(n, "key")+" left unlocked when the app last stopped", "info")
	}
}

func (g *gui) onUnlock() {
	s := g.current()
	if s == nil || s.Kind == "known" || s.KeyLock != lockLocked {
		return
	}
	alias, key, agentOn := s.Alias, filepath.Clean(s.KeyPath), g.data.AgentOn
	g.start("", func() {
		if g.app.t.Add == "" {
			g.notify("ssh-add was not found. It is part of the OpenSSH Client that comes with Windows.", "bad")
			return
		}
		if !agentOn {
			if g.ask(&modal{tone: "warn", icon: icUnlock, title: "Turn on the Windows key agent?", dismiss: true,
				blocks: []mblock{
					{kind: "p", text: "An unlocked key is held by the OpenSSH Authentication Agent, a service that is part of Windows. It is switched off on this computer."},
					{kind: "p", text: "Turning it on needs administrator permission, one time. Windows will ask you."},
					{kind: "muted", text: "This app does not hold your key or your passphrase. The Windows agent keeps an unlocked key, protected for your Windows account, until it is locked again."},
				},
				buttons: []mbtn{{label: "Cancel"}, {label: "Turn it on", v: "go", kind: btnPrimary, icon: icShield}}}).v != "go" {
				return
			}
			g.setStatus("Waiting for the administrator prompt and the key agent…")
			err := g.app.startAgent()
			g.setStatus("")
			if err != nil {
				g.fail(err)
				return
			}
		}
		m := &modal{tone: "info", icon: icUnlock, title: "Unlock the key for " + alias, dismiss: true,
			blocks: []mblock{
				{kind: "p", text: "A terminal window will open and ask for the key's passphrase. It goes straight to ssh. This app never sees or stores it."},
				{kind: "p", text: "Keep the key unlocked:"},
				{kind: "choice", opts: []string{
					"Until I close this app. Closing the app locks the key again.",
					"Until I lock it myself. It stays unlocked after the app closes, and after a restart.",
				}},
				{kind: "muted", text: "While it is unlocked, AI apps on this computer can use the key without asking. They must use Windows' own ssh; the ssh that comes with Git cannot reach an unlocked key."},
			},
			buttons: []mbtn{{label: "Cancel"}, {label: "Open terminal", v: "go", kind: btnPrimary, icon: icTerm}}}
		if loadSettings().UnlockUntil == untilKeep {
			m.choice = 1
		}
		r := g.ask(m)
		if r.v != "go" {
			return
		}
		mode := untilClose
		if r.choice == 1 {
			mode = untilKeep
		}
		st := loadSettings()
		st.UnlockUntil = mode
		if err := saveSettings(st); err != nil {
			g.fail(err)
		}
		wait := &modal{tone: "info", icon: icTerm, title: "Enter the passphrase in the terminal", blocks: []mblock{
			{kind: "p", text: "Type the passphrase for this key in the window that just opened."},
			{kind: "wait", text: "Waiting for you to finish in the terminal window. Close that window to cancel."},
		}}
		g.ui(func() { g.modal = wait })
		err := g.app.unlock(alias)
		g.ui(func() {
			if g.modal == wait {
				g.modal = nil
			}
		})
		if err != nil {
			g.fail(err)
			return
		}
		g.reload("", false)
		now := g.snap(alias)
		if now == nil || now.KeyLock != lockUnlocked {
			g.notify("The key is still locked", "info")
			return
		}
		g.ui(func() {
			if mode == untilClose {
				g.sessionKeys[key] = alias
			} else {
				delete(g.sessionKeys, key)
			}
			g.saveSession()
			if mode == untilClose {
				g.toast("Key unlocked. It will be locked again when you close this app.", "good")
			} else {
				g.toast("Key unlocked until you lock it", "good")
			}
			delete(g.tests, alias)
		})
		g.testFlow(alias, true)
	})
}

func (g *gui) onLock() {
	s := g.current()
	if s == nil || s.Kind == "known" || s.KeyLock != lockUnlocked {
		return
	}
	alias, key := s.Alias, filepath.Clean(s.KeyPath)
	g.start("Locking the key…", func() {
		if err := g.app.lock(alias); err != nil {
			g.fail(err)
			return
		}
		g.ui(func() {
			delete(g.sessionKeys, key)
			g.saveSession()
			// every server that shares this key is affected
			for i := range g.data.Servers {
				if o := &g.data.Servers[i]; o.KeyPath != "" && samePath(o.KeyPath, key) {
					delete(g.tests, o.Alias)
				}
			}
		})
		g.reload("", false)
		g.notify("Key locked. AI apps can't use it until you unlock it.", "good")
	})
}

func (g *gui) onPassphrase() {
	s := g.current()
	if s == nil || s.Kind == "known" || !s.KeyExists {
		return
	}
	alias, key, had := s.Alias, filepath.Clean(s.KeyPath), s.KeyLock != lockNone
	shared := 0
	for i := range g.data.Servers {
		if o := &g.data.Servers[i]; o.Kind != "known" && o.Alias != alias && o.KeyPath != "" && samePath(o.KeyPath, key) {
			shared++
		}
	}
	g.start("", func() {
		title, first := "Add a passphrase to this key?", "A terminal window will open and ask for the new passphrase, twice."
		if had {
			title, first = "Change the passphrase of this key?", "A terminal window will open and ask for the current passphrase, then the new one, twice. Leave the new one empty to remove the passphrase."
		}
		blocks := []mblock{
			{kind: "mono", text: configPathFor(key)},
			{kind: "p", text: first + " It goes straight to ssh-keygen. This app never sees or stores it."},
		}
		if !had {
			blocks = append(blocks, mblock{kind: "p", text: "A key with a passphrase is locked until you unlock it here. AI apps can't use " + alias + " while it is locked."})
		}
		if shared > 0 {
			blocks = append(blocks, mblock{kind: "muted", text: fmt.Sprintf("%s also use this key and are affected in the same way.", plural(shared, "other server"))})
		}
		blocks = append(blocks, mblock{kind: "muted", text: "If you forget the passphrase the key can't be recovered. You would create a new key and install it again."})
		if g.ask(&modal{tone: "warn", icon: icLock, title: title, blocks: blocks, dismiss: true,
			buttons: []mbtn{{label: "Cancel"}, {label: "Open terminal", v: "go", kind: btnPrimary, icon: icTerm}}}).v != "go" {
			return
		}
		wait := &modal{tone: "info", icon: icTerm, title: "Set the passphrase in the terminal", blocks: []mblock{
			{kind: "p", text: "Follow the questions in the window that just opened."},
			{kind: "wait", text: "Waiting for you to finish in the terminal window. Close that window to cancel."},
		}}
		g.ui(func() { g.modal = wait })
		err := g.app.changePassphrase(alias)
		g.ui(func() {
			if g.modal == wait {
				g.modal = nil
			}
		})
		if err != nil {
			g.fail(err)
			return
		}
		g.reload("", false)
		now := g.snap(alias)
		if now == nil {
			return
		}
		g.ui(func() { delete(g.tests, alias) })
		switch now.KeyLock {
		case lockNone:
			if had {
				g.notify("The key no longer has a passphrase", "good")
			} else {
				g.notify("The key was not changed", "info")
			}
		case lockLocked:
			g.notify("The key has a passphrase and is locked. Use Unlock to let AI apps use it.", "good")
		case lockUnlocked:
			g.notify("The passphrase was saved. The key is still unlocked.", "good")
		}
	})
}

// closeFlow runs when the window is closed while keys are unlocked "until I
// close the app": it says so, locks them, and exits.
func (g *gui) closeFlow() {
	var names []string
	var keys []string
	g.ui(func() {
		for k, alias := range g.sessionKeys {
			keys = append(keys, k)
			names = append(names, alias)
		}
	})
	sort.Strings(names)
	what := "the key for " + names[0]
	if len(names) > 1 {
		what = plural(len(names), "key") + " (" + strings.Join(names, ", ") + ")"
	}
	if g.ask(&modal{tone: "warn", icon: icLock, title: "Closing will lock " + what, dismiss: true,
		blocks: []mblock{
			{kind: "p", text: "You unlocked " + what + " until this app closes. Closing now locks it again, and AI apps and ssh can't use it until you open the app and unlock it."},
			{kind: "muted", text: "To keep a key unlocked after closing, choose \"Until I lock it myself\" the next time you unlock it."},
		},
		buttons: []mbtn{{label: "Cancel"}, {label: "Lock and close", v: "go", kind: btnPrimary, icon: icLock}}}).v != "go" {
		return
	}
	g.setStatus("Locking…")
	failed := 0
	for _, k := range keys {
		if err := g.app.t.lockKey(k); err != nil {
			failed++
		}
	}
	if failed > 0 {
		// Leave the list on disk: the next start tries again.
		g.setStatus("")
		g.notify(plural(failed, "key")+" could not be locked. Use Lock on the server, or close again.", "bad")
		g.reload("", false)
		return
	}
	s := loadSettings()
	s.SessionKeys = nil
	saveSettings(s)
	os.Exit(0)
}

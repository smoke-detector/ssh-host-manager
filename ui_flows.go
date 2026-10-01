package main

// Dialogs and what the buttons do. Anything that runs ssh happens on a worker
// goroutine, so the window keeps drawing; the pill at the bottom says what is
// going on, and every step that needs the user asks in a dialog.

import (
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ---------- dialog ----------

type mblock struct {
	kind string // p, muted, keys, code, mono, check, wait
	text string
	keys []OfferedKey
	tags bool // keys: mark the new ones
}

type mbtn struct {
	label, v string
	kind     btnKind
	icon     *widget.Icon
}

type modalResult struct {
	v       string
	checked bool
}

type modal struct {
	tone    string
	icon    *widget.Icon
	title   string
	blocks  []mblock
	buttons []mbtn
	dismiss bool // Escape or a click outside answers ""

	done   chan modalResult
	clicks []widget.Clickable
	check  widget.Bool
	copyB  widget.Clickable
	scrim  widget.Clickable
	inner  widget.Clickable
}

// answer closes the dialog with a result. Call with g.mu held.
func (g *gui) answer(v string, checked bool) {
	if m := g.modal; m != nil {
		g.modal = nil
		if m.done != nil {
			m.done <- modalResult{v, checked}
		}
	}
}

func (g *gui) modalUI(gtx C) {
	m := g.modal
	if len(m.clicks) < len(m.buttons) {
		m.clicks = make([]widget.Clickable, len(m.buttons))
	}
	for i := range m.buttons {
		if m.clicks[i].Clicked(gtx) {
			g.answer(m.buttons[i].v, m.check.Value)
			return
		}
	}
	if m.scrim.Clicked(gtx) && m.dismiss {
		g.answer("", false)
		return
	}
	m.inner.Clicked(gtx)
	// scrim
	m.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, colorScrim, clip.Rect{Max: gtx.Constraints.Max}.Op())
		return D{Size: gtx.Constraints.Max}
	})
	layout.Center.Layout(gtx, func(gtx C) D {
		w := min(gtx.Dp(540), gtx.Constraints.Max.X-gtx.Dp(48))
		gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y-gtx.Dp(48))}
		// The inner clickable swallows clicks so they don't reach the scrim.
		return m.inner.Layout(gtx, func(gtx C) D {
			return box{bg: colModal, border: colLine2, radius: 16}.Layout(gtx, func(gtx C) D {
				kids := []layout.FlexChild{
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 22, Right: 22, Top: 22, Bottom: 4}.Layout(gtx, func(gtx C) D {
							col := toneColor(m.tone)
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									px := gtx.Dp(42)
									r := image.Rectangle{Max: image.Pt(px, px)}
									fillRRect(gtx, r, gtx.Dp(12), alpha(col, .14))
									gtx.Constraints = layout.Exact(r.Max)
									layout.Center.Layout(gtx, func(gtx C) D { return g.icon(gtx, m.icon, 20, col) })
									return D{Size: r.Max}
								}),
								gap(14),
								layout.Flexed(1, g.txt(16.5, m.title, colText, bold).Layout),
							)
						})
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 78, Right: 22, Top: 10, Bottom: 4}.Layout(gtx, func(gtx C) D {
							var rows []layout.FlexChild
							for i := range m.blocks {
								rows = append(rows, layout.Rigid(func(gtx C) D { return g.modalBlock(gtx, m, &m.blocks[i]) }))
							}
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
						})
					}),
				}
				if len(m.buttons) > 0 {
					kids = append(kids, layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 22, Right: 22, Top: 12, Bottom: 20}.Layout(gtx, func(gtx C) D {
							row := []layout.FlexChild{layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} })}
							for i, b := range m.buttons {
								if i > 0 {
									row = append(row, gap(8))
								}
								row = append(row, layout.Rigid(func(gtx C) D {
									return g.button(gtx, &m.clicks[i], btnStyle{kind: b.kind, icon: b.icon, label: b.label})
								}))
							}
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
						})
					}))
				} else {
					kids = append(kids, gap(18))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})
		})
	})
}

var colorScrim = alpha(rgb(0x050609), .66)

func (g *gui) modalBlock(gtx C, m *modal, b *mblock) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	pad := layout.Inset{Bottom: 10}
	switch b.kind {
	case "p":
		return pad.Layout(gtx, g.txt(13.5, b.text, rgb(0xc3c9d4)).Layout)
	case "muted":
		return pad.Layout(gtx, g.txt(12.5, b.text, colMuted).Layout)
	case "keys":
		return layout.Inset{Top: 2, Bottom: 12}.Layout(gtx, func(gtx C) D {
			return box{bg: colInset, border: colLine, radius: 10}.Layout(gtx, func(gtx C) D {
				var rows []layout.FlexChild
				for i, k := range b.keys {
					if i > 0 {
						rows = append(rows, layout.Rigid(g.divider))
					}
					rows = append(rows, layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 12, Right: 12, Top: 10, Bottom: 10}.Layout(gtx, func(gtx C) D {
							row := []layout.FlexChild{
								layout.Rigid(func(gtx C) D { return g.badge(gtx, keyTypeLabel(k.Type), 62) }), gap(10),
								layout.Flexed(1, g.txt(12, k.Fp, rgb(0xcfd5df), monoFont).Layout),
							}
							if b.tags && k.IsNew {
								row = append(row, gap(8), layout.Rigid(func(gtx C) D {
									return box{bg: alpha(colWarn, .12), radius: 6, in: layout.Inset{Left: 7, Right: 7, Top: 3, Bottom: 3}}.Layout(gtx,
										g.txt(10, "NEW", colWarn, bold).Layout)
								}))
							}
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
						})
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
			})
		})
	case "code":
		if m.copyB.Clicked(gtx) {
			g.copy(b.text, "Copied")
		}
		return pad.Layout(gtx, func(gtx C) D {
			return box{bg: colInset, border: colLine, radius: 9, in: layout.Inset{Left: 12, Right: 4, Top: 4, Bottom: 4}}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, g.txt(12, b.text, rgb(0xcfd5df), monoFont, oneLine).Layout),
					layout.Rigid(func(gtx C) D { return g.button(gtx, &m.copyB, btnStyle{kind: btnGhost, small: true, icon: icCopy}) }),
				)
			})
		})
	case "mono":
		return layout.Inset{Top: 2, Bottom: 12}.Layout(gtx, func(gtx C) D {
			return box{bg: colInset, border: colLine, radius: 10, in: layout.Inset{Left: 12, Right: 12, Top: 10, Bottom: 10}}.Layout(gtx, func(gtx C) D {
				l := g.txt(12, b.text, rgb(0xcfd5df), monoFont)
				l.MaxLines = 7
				return l.Layout(gtx)
			})
		})
	case "check":
		return layout.Inset{Top: 2, Bottom: 12}.Layout(gtx, func(gtx C) D {
			return box{bg: colInset, border: colLine2, radius: 10, in: layout.Inset{Left: 6, Right: 12, Top: 2, Bottom: 2}}.Layout(gtx, func(gtx C) D {
				cb := material.CheckBox(g.th, &m.check, b.text)
				cb.Color, cb.IconColor, cb.TextSize, cb.Size = rgb(0xc3c9d4), colAccent, 13, 20
				return cb.Layout(gtx)
			})
		})
	case "wait":
		return pad.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return g.spinner(gtx, 16) }), gap(12),
				layout.Flexed(1, g.txt(13, b.text, colMuted).Layout))
		})
	}
	return D{}
}

// notice shows the disclaimer. On first run it must be accepted before the
// app does anything; later it can be read again from the corner of the window.
func (g *gui) notice(first bool) {
	blocks := make([]mblock, 0, len(noticeText)+1)
	for _, p := range noticeText {
		blocks = append(blocks, mblock{kind: "p", text: p})
	}
	blocks = append(blocks, mblock{kind: "muted", text: noticeSource})
	if !first {
		g.ask(&modal{tone: "warn", icon: icInfo, title: "Disclaimer", blocks: blocks, dismiss: true, buttons: []mbtn{{label: "Close"}}})
		return
	}
	if g.ask(&modal{tone: "warn", icon: icInfo, title: "Before you start", blocks: blocks,
		buttons: []mbtn{{label: "Quit"}, {label: "I understand", v: "ok", kind: btnPrimary, icon: icCheck}}}).v != "ok" {
		os.Exit(0)
	}
	s := loadSettings()
	s.Accepted = noticeVersion
	if err := saveSettings(s); err != nil {
		g.fail(err)
	}
}

// ---------- plumbing between workers and the window ----------

// ui changes state from a worker and asks for a redraw.
func (g *gui) ui(f func()) {
	g.mu.Lock()
	f()
	g.mu.Unlock()
	g.w.Invalidate()
}

// start runs a flow on a worker with the buttons that change things switched
// off. Call with g.mu held (from the frame).
func (g *gui) start(status string, flow func()) {
	if g.busy {
		return
	}
	g.busy, g.status = true, status
	go func() {
		defer func() {
			r := recover()
			g.ui(func() {
				g.busy, g.status = false, ""
				if r != nil {
					g.toast(fmt.Sprint("Something went wrong: ", r), "bad")
				}
			})
		}()
		flow()
	}()
}

func (g *gui) setStatus(s string) { g.ui(func() { g.status = s }) }
func (g *gui) notify(msg, tone string) {
	g.ui(func() { g.toast(msg, tone) })
}
func (g *gui) fail(err error) { g.notify(capFirst(err.Error()), "bad") }

// try reports an error from a quick action (g.mu held).
func (g *gui) try(err error) {
	if err != nil {
		g.toast(capFirst(err.Error()), "bad")
	}
}

// ask shows a dialog and waits for the answer. Workers only.
func (g *gui) ask(m *modal) modalResult {
	m.done = make(chan modalResult, 1)
	g.ui(func() { g.modal = m })
	return <-m.done
}

// snap copies one server's current state for a worker to read.
func (g *gui) snap(alias string) *ServerView {
	var out *ServerView
	g.ui(func() {
		if s := g.server(alias); s != nil {
			c := *s
			out = &c
		}
	})
	return out
}

func (g *gui) reload(selectAlias string, refill bool) error {
	sv, err := g.app.state()
	if err != nil {
		return err
	}
	g.ui(func() { g.apply(sv, selectAlias, refill) })
	return nil
}

// watchFiles reloads the window when the config or known_hosts change on disk
// (edited by hand, or by ssh itself).
func (g *gui) watchFiles() {
	for range time.Tick(2500 * time.Millisecond) {
		var old [2]int64
		var cfg, kh string
		skip := false
		g.ui(func() { old, cfg, kh, skip = g.stamps, g.app.t.Config, g.app.t.Known, g.busy || g.modal != nil })
		if skip || old == [2]int64{fileStamp(cfg), fileStamp(kh)} {
			continue
		}
		if sv, err := g.app.state(); err == nil {
			g.ui(func() {
				if !g.busy && g.modal == nil {
					g.apply(sv, "", false)
				}
			})
		}
	}
}

// autoTest checks every server that is fully set up, a few at a time.
func (g *gui) autoTest() {
	var aliases []string
	g.ui(func() {
		for _, s := range g.data.Servers {
			if s.Kind == "managed" && len(s.HostKeys) > 0 && s.KeyExists {
				aliases = append(aliases, s.Alias)
			}
		}
	})
	queue := make(chan string, len(aliases))
	for _, a := range aliases {
		queue <- a
	}
	close(queue)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for alias := range queue {
				g.ui(func() { g.tests[alias] = TestResult{Kind: "checking"} })
				r := g.app.t.test(alias)
				g.ui(func() { g.tests[alias] = r })
			}
		}()
	}
	wg.Wait()
}

// ---------- selection and form buttons (g.mu held) ----------

func (g *gui) onAdd() {
	if g.busy || g.sel.kind == "new" {
		return
	}
	g.selectItem(selection{kind: "new"})
}

func (g *gui) onAddKnown() {
	s := g.current()
	if s == nil || s.Kind != "known" {
		return
	}
	f := formFor(nil)
	f.Host, f.Port = s.Host, s.Port
	g.selectItem(selection{kind: "new"})
	g.fillForm(f)
}

func (g *gui) onDiscard() {
	if g.sel.kind == "new" {
		g.sel = selection{}
		g.apply(g.data, "", true)
		return
	}
	if s := g.current(); s != nil {
		g.fillForm(formFor(s))
	}
}

func (g *gui) onCopyPub(alias string) {
	pub, err := g.app.pubkey(AliasReq{Alias: alias})
	if err != nil {
		g.try(err)
		return
	}
	g.copy(pub, "Public key copied")
}

func (g *gui) onCopyAI() {
	var lines []string
	for i := range g.data.Servers {
		s := &g.data.Servers[i]
		if s.Kind == "known" {
			continue
		}
		tone, label := g.statusOf(s)
		flag := " (not verified yet)"
		switch {
		case g.tests[s.Alias].Kind == "ok":
			flag = ""
		case tone == "bad" || tone == "warn":
			flag = " (currently not working: " + strings.ToLower(label) + ")"
		}
		lines = append(lines, "- "+s.Alias+": "+target(s)+flag)
	}
	if len(lines) == 0 {
		g.toast("Add a server first", "info")
		return
	}
	g.copy("I have SSH access set up from this computer to the servers below. Run commands with `ssh <name> \"<command>\"`; "+
		"the IP address works in place of the name. Logins use keys, so there are no password or host-key prompts.\n\n"+
		strings.Join(lines, "\n")+"\n", "Copied. Paste it into Claude or ChatGPT.")
}

func (g *gui) onDonate() {
	if err := openURL(links["donate"]); err != nil {
		g.try(err)
		return
	}
	g.toast("Opened the donation page in your web browser. Thank you!", "good")
}

func (g *gui) onReload() {
	g.start("Reloading…", func() {
		if err := g.reload("", true); err != nil {
			g.fail(err)
			return
		}
		g.notify("Reloaded config and known_hosts", "info")
	})
}

// ---------- save, verify, test, install ----------

func (g *gui) saveFlow(f formVals) {
	orig := ""
	var before *ServerView
	if s := g.current(); g.sel.kind == "server" && s != nil {
		c := *s
		before, orig = &c, s.Alias
	}
	isNew := g.sel.kind == "new"
	status := "Saving…"
	if isNew {
		status = "Saving the server and creating its key…"
	}
	g.start(status, func() { g.saveAndContinue(f, orig, before, isNew) })
}

// saveAndContinue saves, then carries on with whatever the server still
// needs: its fingerprint, a login test, the key install.
func (g *gui) saveAndContinue(f formVals, orig string, before *ServerView, isNew bool) {
	res, err := g.app.save(SaveReq{Original: orig, Alias: f.Alias, Host: f.Host, Port: f.Port, User: f.User,
		IdentityFile: f.Key, Device: f.Device, Legacy: f.Legacy})
	if err != nil {
		g.fail(err)
		if err == errStale {
			g.reload("", false)
		}
		return
	}
	if err := g.reload(res.Alias, true); err != nil {
		g.fail(err)
		return
	}
	g.ui(func() {
		if res.CreatedKey != "" {
			g.toast("Created key "+res.CreatedKey, "good")
		}
		if res.RemovedHostKey != "" {
			g.toast("Removed old host key for "+res.RemovedHostKey, "info")
		}
		if isNew {
			g.toast("Added "+res.Alias, "good")
		} else {
			g.toast("Saved "+res.Alias, "good")
		}
	})
	s := g.snap(res.Alias)
	if s == nil {
		return
	}
	moved := before == nil || before.Host != s.Host || before.Port != s.Port
	if len(s.HostKeys) == 0 || moved {
		switch g.verifyHostKey(s.Host, s.Port, true, true) {
		case "unreachable", "declined", "error":
			return
		}
	}
	g.ui(func() { delete(g.tests, res.Alias) })
	g.testFlow(res.Alias, true)
}

// verifyHostKey reads the server's host keys and asks before trusting them.
// It returns unreachable, unchanged, declined, trusted or error.
func (g *gui) verifyHostKey(host, port string, managed, quiet bool) string {
	name := addr(host, port)
	var r *ScanRes
	for {
		g.setStatus("Contacting " + name + " to read its fingerprint…")
		var err error
		r, err = g.app.scan(HostReq{Host: host, Port: port})
		g.setStatus("")
		if err != nil {
			g.fail(err)
			return "error"
		}
		if r.Status != "unreachable" {
			break
		}
		msg := capFirst(r.Message)
		if msg == "" {
			msg = "The device didn't answer"
		}
		blocks := []mblock{{kind: "p", text: strings.TrimSuffix(msg, ".") + "."}}
		if managed {
			blocks = append(blocks, mblock{kind: "muted", text: "Nothing is lost. The server and its key are saved. When the device can be reached, " +
				"press Verify host key and the setup continues from here."})
		}
		if g.ask(&modal{tone: "bad", icon: icAlert, title: "Couldn't reach " + name, blocks: blocks, dismiss: true,
			buttons: []mbtn{{label: "Close"}, {label: "Try again", v: "retry", kind: btnPrimary, icon: icRefresh}}}).v != "retry" {
			return "unreachable"
		}
	}
	if r.Status == "unchanged" {
		if !quiet {
			g.notify("Host key for "+name+" is unchanged", "good")
		}
		return "unchanged"
	}
	keys := append([]OfferedKey{}, r.Offered...)
	m := &modal{dismiss: true}
	text, btn := "", ""
	switch r.Status {
	case "new":
		m.tone, m.icon, m.title = "info", icShield, "Verify "+name
		text, btn = "This is the first time this computer connects to this server. Check that the fingerprint matches the server before trusting it.", "Trust server"
	case "added":
		m.tone, m.icon, m.title = "warn", icShield, name+" offers new host keys"
		text, btn = "The server still has the key you trusted, plus keys you haven't seen before. This usually follows an OpenSSH upgrade.", "Trust all"
	default:
		m.tone, m.icon, m.title = "bad", icAlert, "Host key changed for "+name
		text, btn = "The server presented different keys than the ones you trusted. That's expected if it was rebuilt or reinstalled. "+
			"If not, someone could be intercepting the connection.", "Replace and trust"
	}
	kind := btnPrimary
	if r.Status == "changed" {
		kind = btnDangerSolid
	}
	m.blocks = []mblock{
		{kind: "p", text: text},
		{kind: "keys", keys: keys, tags: r.Status != "new"},
		{kind: "muted", text: "To check, run this on the server and compare:"},
		{kind: "code", text: "ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub"},
	}
	m.buttons = []mbtn{{label: "Cancel"}, {label: btn, v: "trust", kind: kind, icon: icShield}}
	if g.ask(m).v != "trust" {
		g.notify("Host key not trusted", "info")
		return "declined"
	}
	g.setStatus("Saving the fingerprint…")
	err := g.app.trust(HostReq{Host: host, Port: port})
	g.setStatus("")
	if err != nil {
		g.fail(err)
		return "error"
	}
	g.reload("", false)
	g.notify("Trusted host key for "+name, "good")
	return "trusted"
}

// testFlow tries a passwordless login. With offer set it goes on to whatever
// the result calls for.
func (g *gui) testFlow(alias string, offer bool) TestResult {
	g.ui(func() {
		g.tests[alias] = TestResult{Kind: "checking"}
		g.status = "Testing the login to " + alias + "…"
	})
	r := g.app.t.test(alias)
	g.ui(func() {
		g.tests[alias] = r
		g.status = ""
	})
	if !offer {
		return r
	}
	switch r.Kind {
	case "auth":
		g.installFlow(alias)
	case "algo":
		g.legacyFlow(alias)
	}
	return r
}

func (g *gui) installFlow(alias string) {
	s := g.snap(alias)
	if s == nil {
		return
	}
	if s.IdentityFile == "" || !s.KeyExists {
		g.notify("This server has no key file. Manage it with the app first.", "info")
		return
	}
	if s.Device == devManual {
		g.manualFlow(s)
		return
	}
	user := s.User
	if user == "" {
		g.ui(func() { user = g.data.LocalUser })
	}
	who := user + "@" + s.Host
	if g.ask(&modal{tone: "info", icon: icTerm, title: "Install your key on " + alias, dismiss: true,
		blocks: []mblock{
			{kind: "p", text: "A terminal window will open and ask for the password for " + who + ", one time. After that, logins use the key."},
			{kind: "muted", text: "The password goes straight to ssh. This app never sees or stores it."},
		},
		buttons: []mbtn{{label: "Not now"}, {label: "Open terminal", v: "go", kind: btnPrimary, icon: icTerm}}}).v != "go" {
		return
	}
	wait := &modal{tone: "info", icon: icTerm, title: "Enter the password in the terminal", blocks: []mblock{
		{kind: "p", text: "Type the password for " + who + " in the window that just opened. It closes by itself when ssh is done."},
		{kind: "wait", text: "Waiting for you to finish in the terminal window. Close that window to cancel."},
	}}
	g.ui(func() { g.modal = wait })
	err := g.app.install(AliasReq{Alias: alias})
	g.ui(func() {
		if g.modal == wait {
			g.modal = nil
		}
	})
	if err != nil {
		g.fail(err)
		return
	}
	g.notify("Key installed on "+alias, "good")
	g.testFlow(alias, false)
}

// manualFlow shows the public key for devices that need it pasted in by hand.
func (g *gui) manualFlow(s *ServerView) {
	pub, err := g.app.pubkey(AliasReq{Alias: s.Alias})
	if err != nil {
		g.fail(err)
		return
	}
	user := s.User
	if user == "" {
		user = "you log in as"
	}
	if g.ask(&modal{tone: "info", icon: icKey, title: "Add your key to " + s.Alias, dismiss: true,
		blocks: []mblock{
			{kind: "p", text: "This device type can't take the key automatically. Copy the public key and add it on the device for the user " + user +
				", in the place where it manages SSH public keys (its web page or its command line)."},
			{kind: "mono", text: pub},
			{kind: "muted", text: "Then press Retest. A public key is safe to paste; the private key never leaves this computer."},
		},
		buttons: []mbtn{{label: "Close"}, {label: "Copy public key", v: "copy", kind: btnPrimary, icon: icCopy}}}).v == "copy" {
		g.ui(func() { g.copy(pub, "Public key copied") })
	}
}

func (g *gui) legacyFlow(alias string) {
	s := g.snap(alias)
	if s == nil || s.Kind != "managed" || s.Legacy {
		return
	}
	muted := "Other servers are not affected."
	if s.KeyType != "rsa" {
		muted += " The server also gets an RSA key, because devices this old don't accept newer key types."
	}
	if g.ask(&modal{tone: "warn", icon: icShield, title: alias + " needs older encryption", dismiss: true,
		blocks: []mblock{
			{kind: "p", text: "This device only supports encryption methods that ssh turns off by default. The app can allow them for this one server."},
			{kind: "muted", text: muted},
		},
		buttons: []mbtn{{label: "Not now"}, {label: "Allow and retry", v: "go", kind: btnPrimary, icon: icShield}}}).v != "go" {
		return
	}
	f := formFor(s)
	f.Legacy = true
	g.ui(func() { g.applyLegacy(&f) })
	g.setStatus("Saving…")
	g.saveAndContinue(f, alias, s, false)
}

func (g *gui) onTest() {
	if s := g.current(); s != nil && s.Kind != "known" {
		alias := s.Alias
		g.start("", func() { g.testFlow(alias, true) })
	}
}

func (g *gui) onInstall() {
	if s := g.current(); s != nil && s.Kind != "known" {
		alias := s.Alias
		g.start("", func() { g.installFlow(alias) })
	}
}

func (g *gui) onLegacy() {
	if s := g.current(); s != nil && s.Kind == "managed" {
		alias := s.Alias
		g.start("", func() { g.legacyFlow(alias) })
	}
}

func (g *gui) onVerify() {
	if s := g.current(); s != nil && s.Kind != "known" {
		alias, host, port, managed := s.Alias, s.Host, s.Port, s.Kind == "managed"
		g.start("", func() {
			if g.verifyHostKey(host, port, managed, false) == "trusted" {
				g.ui(func() { delete(g.tests, alias) })
				g.testFlow(alias, true)
			}
		})
	}
}

func (g *gui) onRescan() {
	s := g.current()
	if s == nil {
		return
	}
	alias, host, port, kind := s.Alias, s.Host, s.Port, s.Kind
	g.start("", func() {
		if g.verifyHostKey(host, port, kind == "managed", false) == "trusted" && kind != "known" {
			g.ui(func() { delete(g.tests, alias) })
			g.testFlow(alias, false)
		}
	})
}

// ---------- delete, host keys ----------

func (g *gui) onDelete() {
	s := g.current()
	if s == nil || s.Kind != "managed" {
		return
	}
	alias, ident := s.Alias, s.IdentityFile
	shared := false
	for i := range g.data.Servers {
		o := &g.data.Servers[i]
		if o.Kind != "known" && o.Alias != alias && o.KeyPath != "" && s.KeyPath != "" && samePathText(o.KeyPath, s.KeyPath) {
			shared = true
		}
	}
	canDelKey := s.KeyExists && !shared
	g.start("", func() {
		blocks := []mblock{{kind: "p", text: "Removes it from your SSH config and its host key from known_hosts."}}
		if canDelKey {
			blocks = append(blocks, mblock{kind: "check", text: "Also delete the key file " + ident})
		}
		blocks = append(blocks, mblock{kind: "muted", text: "Its public key stays in the server's authorized_keys. Remove it there to fully revoke access."})
		r := g.ask(&modal{tone: "bad", icon: icTrash, title: "Delete " + alias + "?", blocks: blocks, dismiss: true,
			buttons: []mbtn{{label: "Cancel"}, {label: "Delete server", v: "del", kind: btnDangerSolid}}})
		if r.v != "del" {
			return
		}
		g.setStatus("Deleting " + alias + "…")
		d, err := g.app.del(DeleteReq{Alias: alias, DeleteKey: canDelKey && r.checked})
		if err != nil {
			g.fail(err)
			if err == errStale {
				g.reload("", false)
			}
			return
		}
		g.ui(func() {
			delete(g.tests, alias)
			g.sel = selection{}
		})
		g.reload("", true)
		msg := "Deleted " + alias
		if d.DeletedKey != "" {
			msg += " and its key file"
		}
		g.notify(msg, "good")
		if d.KeptHostKeyFor != "" {
			g.notify("Kept the host key, "+d.KeptHostKeyFor+" still uses it", "info")
		}
	})
}

func (g *gui) removeKeys(s ServerView, fp, done string) {
	if err := g.app.removeHostKey(HostReq{Host: s.Host, Port: s.Port, Fp: fp}); err != nil {
		g.fail(err)
		return
	}
	if s.Kind != "known" {
		g.ui(func() { delete(g.tests, s.Alias) })
	}
	g.reload("", false)
	g.notify(done, "good")
}

func (g *gui) onRemoveOne(fp string) {
	cur := g.current()
	if cur == nil {
		return
	}
	s := *cur
	var k *HostKey
	for i := range s.HostKeys {
		if s.HostKeys[i].Fp == fp {
			k = &s.HostKeys[i]
		}
	}
	if k == nil {
		return
	}
	name := addr(s.Host, s.Port)
	g.start("", func() {
		blocks := []mblock{{kind: "mono", text: k.Fp}}
		switch {
		case len(s.HostKeys) > 2:
			blocks = append(blocks, mblock{kind: "p", text: "The other trusted keys stay in place."})
		case len(s.HostKeys) == 2:
			blocks = append(blocks, mblock{kind: "p", text: "The other trusted key stays in place."})
		case s.Kind != "known":
			blocks = append(blocks, mblock{kind: "p", text: "This is the only trusted key, so ssh " + s.Alias + " will refuse to connect until you verify the host key again."})
		}
		blocks = append(blocks, mblock{kind: "muted", text: "The previous file is kept as known_hosts.old."})
		if g.ask(&modal{tone: "warn", icon: icTrash, title: "Remove the " + keyTypeLabel(k.Type) + " key for " + name + "?", blocks: blocks, dismiss: true,
			buttons: []mbtn{{label: "Cancel"}, {label: "Remove this key", v: "rm", kind: btnDangerSolid}}}).v != "rm" {
			return
		}
		g.removeKeys(s, k.Fp, "Removed the "+keyTypeLabel(k.Type)+" key for "+name)
	})
}

func (g *gui) onRemoveAll() {
	cur := g.current()
	if cur == nil || len(cur.HostKeys) == 0 {
		return
	}
	s := *cur
	name := addr(s.Host, s.Port)
	g.start("", func() {
		var blocks []mblock
		if s.Kind != "known" {
			blocks = append(blocks, mblock{kind: "p", text: "ssh " + s.Alias + " and your AI apps will refuse to connect until you verify the host key again."})
		}
		blocks = append(blocks, mblock{kind: "muted", text: "The previous file is kept as known_hosts.old."})
		if g.ask(&modal{tone: "warn", icon: icTrash, title: "Remove host key for " + name + "?", blocks: blocks, dismiss: true,
			buttons: []mbtn{{label: "Cancel"}, {label: "Remove host key", v: "rm", kind: btnDangerSolid}}}).v != "rm" {
			return
		}
		g.removeKeys(s, "", "Removed host key for "+name)
	})
}

// ---------- folder ----------

func (g *gui) onFolder(path string) {
	active := g.data.SSHDir
	g.start("", func() {
		if path == "" {
			p, err := pickFolder("Choose the folder for your SSH config and keys")
			if err != nil {
				g.fail(err)
			}
			if p == "" {
				return
			}
			path = p
		}
		if samePath(path, active) {
			return
		}
		if g.ask(&modal{tone: "info", icon: icFolder, title: "Use this folder?", dismiss: true,
			blocks: []mblock{
				{kind: "mono", text: path},
				{kind: "p", text: "The app will keep its server list, keys and known_hosts here."},
				{kind: "muted", text: "ssh only reads ~/.ssh/config by itself, so the app adds one line there that points to this folder. " +
					"Servers saved in the current folder stay where they are and are no longer listed."},
			},
			buttons: []mbtn{{label: "Cancel"}, {label: "Use this folder", v: "go", kind: btnPrimary, icon: icFolder}}}).v != "go" {
			return
		}
		g.setStatus("Switching folder…")
		if err := g.app.setFolder(FolderReq{Path: path}); err != nil {
			g.fail(err)
		}
		g.ui(func() {
			g.tests = map[string]TestResult{}
			g.sel = selection{}
		})
		if err := g.reload("", true); err != nil {
			g.fail(err)
			return
		}
		g.notify("Now using "+configPathFor(path), "good")
		go g.autoTest()
	})
}

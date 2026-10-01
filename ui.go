package main

// The window. One goroutine draws it; ssh work runs on workers that change
// the state under g.mu and ask for a redraw.

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font/gofont"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/sys/windows"
)

// The promise shown in the window; the manual and README carry the same one.
const privacyLine = "This app only talks to the servers you add. It does not talk to anything in the cloud or any other service, " +
	"except the donation link if you click it, because my AI is breaking my bank. The source code is public so anyone can check how it works."

// selection is what the right-hand side shows.
type selection struct {
	kind  string // "" (welcome), "new", "server", "known"
	alias string
}

// formVals is the content of the Connection form.
type formVals struct {
	Alias, Host, Port, User string
	Key                     string // a path, "new:ed25519", "new:rsa", or "" (none set / device default)
	Device                  string
	Legacy                  bool
}

type toast struct {
	msg, tone string
	until     time.Time
}

type gui struct {
	app *App
	w   *app.Window
	th  *material.Theme
	mu  sync.Mutex // guards everything below; held while a frame is drawn

	data    *StateView
	tests   map[string]TestResult // Kind "checking" while a test runs
	sel     selection
	stamps  [2]int64
	busy    bool   // a flow is running: buttons that change things are off
	status  string // what the running flow is doing
	toasts  []toast
	modal   *modal
	openSel *selectBox
	clip    string // text waiting to be put on the clipboard
	started bool
	ptr     f32.Point   // where the mouse pointer last was, in window pixels
	winSize image.Point // the window's drawing area
	ptrTag  bool

	search               widget.Editor
	sideList, mainList   widget.List
	showOther, showKnown bool
	clicks               map[string]*widget.Clickable
	selScrim             widget.Clickable
	folderSel            selectBox

	fName, fHost, fPort, fUser widget.Editor
	fKey, fDevice, fLegacy     selectBox
	fv                         formVals // key, device, legacy as chosen in the form
	keyLabels, keyValues       []string
	formLocked                 bool
}

func newGUI(a *App) *gui {
	g := &gui{app: a, tests: map[string]TestResult{}, clicks: map[string]*widget.Clickable{}, showOther: true}
	g.th = material.NewTheme()
	g.th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	g.th.Face = sans
	g.th.Palette = material.Palette{Bg: colBg, Fg: colText, ContrastBg: colAccent, ContrastFg: rgb(0xffffff)}
	for _, e := range []*widget.Editor{&g.search, &g.fName, &g.fHost, &g.fPort, &g.fUser} {
		e.SingleLine, e.Submit = true, true
	}
	g.sideList.Axis, g.mainList.Axis = layout.Vertical, layout.Vertical
	g.folderSel.up = true
	return g
}

// clk returns the click state for a dynamic control (a list row, a per-key button).
func (g *gui) clk(id string) *widget.Clickable {
	c := g.clicks[id]
	if c == nil {
		c = new(widget.Clickable)
		g.clicks[id] = c
	}
	return c
}

// testHook lets an automated test drive the window; nil in normal builds.
var testHook func(*gui)

func main() {
	tools, err := newTools()
	if err != nil {
		fatalBox(err.Error())
		return
	}
	g := newGUI(newApp(tools))
	sv, err := g.app.state()
	if err != nil {
		fatalBox(err.Error())
		return
	}
	g.apply(sv, "", true)
	go func() {
		g.w = new(app.Window)
		g.w.Option(app.Title(appName), app.Size(1200, 800), app.MinSize(980, 660))
		err := g.run()
		if err != nil {
			fatalBox(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func fatalBox(msg string) {
	t, _ := windows.UTF16PtrFromString(appName)
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_ICONERROR)
}

var (
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// darkTitleBar matches the window frame to the app's dark colours.
func darkTitleBar(hwnd uintptr) {
	if procDwmSetWindowAttribute.Find() != nil {
		return
	}
	on := int32(1)
	procDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&on)), 4)
	caption := uint32(0x00191410) // #101419 as 0x00BBGGRR (Windows 11)
	procDwmSetWindowAttribute.Call(hwnd, 35, uintptr(unsafe.Pointer(&caption)), 4)
}

func (g *gui) run() error {
	var ops op.Ops
	for {
		switch e := g.w.Event().(type) {
		case app.Win32ViewEvent:
			if e.HWND != 0 {
				mainHwnd = e.HWND
				darkTitleBar(e.HWND)
			}
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			g.frame(gtx)
			e.Frame(gtx.Ops)
			if !g.started {
				g.started = true
				go func() {
					// Nothing connects anywhere until the notice has been accepted.
					if loadSettings().Accepted != noticeVersion {
						g.notice(true)
					}
					g.autoTest()
				}()
				go g.watchFiles()
				if testHook != nil {
					go testHook(g)
				}
			}
		}
	}
}

// ---------- small helpers ----------

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func addr(host, port string) string {
	if port != "" && port != "22" {
		return host + ":" + port
	}
	return host
}

func target(s *ServerView) string {
	t := addr(s.Host, s.Port)
	if s.User != "" {
		t = s.User + "@" + t
	}
	return t
}

func keyTypeLabel(t string) string {
	switch {
	case t == "ssh-ed25519":
		return "ED25519"
	case t == "ssh-rsa":
		return "RSA"
	case t == "ssh-dss":
		return "DSA"
	case strings.HasPrefix(t, "ecdsa"):
		return "ECDSA"
	case strings.HasPrefix(t, "sk-"):
		return "FIDO"
	}
	return strings.ToUpper(t)
}

func keyRank(t string) int {
	switch {
	case t == "ssh-ed25519":
		return 0
	case strings.HasPrefix(t, "ecdsa"):
		return 1
	case strings.HasPrefix(t, "sk-"):
		return 2
	}
	return 3
}

func byPref(keys []HostKey) []HostKey {
	out := append([]HostKey{}, keys...)
	sort.SliceStable(out, func(i, j int) bool { return keyRank(out[i].Type) < keyRank(out[j].Type) })
	return out
}

func samePathText(a, b string) bool { return strings.EqualFold(configPathFor(a), configPathFor(b)) }

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var devices = []struct{ id, label, help string }{
	{devLinux, "Linux, macOS, NAS (most servers)", "The app adds the key to ~/.ssh/authorized_keys for you."},
	{devESXi, "VMware ESXi", "The app adds the key where ESXi looks for it (/etc/ssh/keys-<user>). ESXi needs an RSA key."},
	{devWindows, "Windows (OpenSSH Server)", "The app adds the key for the account, including administrator accounts."},
	{devManual, "Switch, router, firewall, other", "You paste the public key into the device yourself. The app shows it to you."},
}

// ---------- state lookups (call with g.mu held) ----------

func (g *gui) server(alias string) *ServerView {
	if g.data == nil {
		return nil
	}
	for i := range g.data.Servers {
		if s := &g.data.Servers[i]; s.Kind != "known" && s.Alias == alias {
			return s
		}
	}
	return nil
}

func (g *gui) known(alias string) *ServerView {
	if g.data == nil {
		return nil
	}
	for i := range g.data.Servers {
		if s := &g.data.Servers[i]; s.Kind == "known" && s.Alias == alias {
			return s
		}
	}
	return nil
}

func (g *gui) current() *ServerView {
	switch g.sel.kind {
	case "server":
		return g.server(g.sel.alias)
	case "known":
		return g.known(g.sel.alias)
	}
	return nil
}

func (g *gui) statusOf(s *ServerView) (tone, label string) {
	if s.Kind == "known" {
		return "muted", "Not in config"
	}
	t := g.tests[s.Alias]
	switch {
	case t.Kind == "checking":
		return "busy", "Checking"
	case len(s.HostKeys) == 0:
		return "warn", "Host key missing"
	case s.Kind == "managed" && !s.KeyExists:
		return "warn", "Key file missing"
	case t.Kind == "ok":
		return "good", "Ready"
	case t.Kind == "auth":
		return "bad", "Key not installed"
	case t.Kind == "hostkey":
		return "bad", "Host key rejected"
	case t.Kind == "algo":
		return "bad", "Needs older encryption"
	case t.Kind == "error":
		return "bad", "Unreachable"
	}
	return "muted", "Not tested"
}

// ---------- form ----------

func formFor(s *ServerView) formVals {
	if s == nil {
		return formVals{Port: "22", Key: "new:ed25519", Device: devLinux}
	}
	return formVals{Alias: s.Alias, Host: s.Host, Port: s.Port, User: s.User, Key: s.IdentityFile, Device: normDevice(s.Device), Legacy: s.Legacy}
}

// formKeyType is the kind of key the form points at ("ed25519", "rsa", ...).
func (g *gui) formKeyType(f formVals) string {
	if k, ok := strings.CutPrefix(f.Key, "new:"); ok {
		return k
	}
	if f.Key == "" || g.data == nil {
		return ""
	}
	for _, k := range g.data.Keys {
		if samePathText(k.Path, f.Key) {
			return k.Type
		}
	}
	return ""
}

// applyDevice sets the key type and encryption that suit a device type.
func (g *gui) applyDevice(f *formVals) {
	needRSA := f.Device == devESXi || f.Device == devManual
	if needRSA && g.formKeyType(*f) != "rsa" {
		f.Key = "new:rsa"
	}
	if !needRSA && f.Key == "new:rsa" {
		f.Key = "new:ed25519"
	}
	if f.Device == devManual {
		f.Legacy = true
	}
	if f.Device == devESXi && f.User == "" {
		f.User = "root"
	}
}

// applyLegacy: devices that need older encryption don't know ed25519 keys either.
func (g *gui) applyLegacy(f *formVals) {
	if f.Legacy && g.formKeyType(*f) != "rsa" {
		f.Key = "new:rsa"
	}
}

func (g *gui) readForm() formVals {
	f := g.fv
	f.Alias, f.Host = strings.TrimSpace(g.fName.Text()), strings.TrimSpace(g.fHost.Text())
	f.Port, f.User = strings.TrimSpace(g.fPort.Text()), strings.TrimSpace(g.fUser.Text())
	return f
}

func (g *gui) fillForm(f formVals) {
	g.fName.SetText(f.Alias)
	g.fHost.SetText(f.Host)
	g.fPort.SetText(f.Port)
	g.fUser.SetText(f.User)
	g.fv = f
}

// keyChoices builds the Key file dropdown for the form as it is now.
func (g *gui) keyChoices() (labels []string, current int) {
	g.keyValues = g.keyValues[:0]
	add := func(label, value string) {
		labels = append(labels, label)
		g.keyValues = append(g.keyValues, value)
	}
	cur := g.fv.Key
	if g.formLocked {
		if cur == "" {
			add("None set (ssh uses your default keys)", "")
		}
	} else {
		if cur == "" {
			add("Create a new key for this server", "")
		}
		add("Create a new key: ED25519 (recommended)", "new:ed25519")
		add("Create a new key: RSA (ESXi and older devices)", "new:rsa")
	}
	found := cur == "" || strings.HasPrefix(cur, "new:")
	if g.data != nil {
		for _, k := range g.data.Keys {
			label := k.Path
			if k.Type != "" {
				label += " (" + strings.ToUpper(k.Type) + ")"
			}
			add(label, k.Path)
			if !found && samePathText(k.Path, cur) {
				found = true
			}
		}
	}
	if !found {
		add(cur+" (missing)", cur)
	}
	for i, v := range g.keyValues {
		if v == cur || (v != "" && cur != "" && !strings.HasPrefix(v, "new:") && !strings.HasPrefix(cur, "new:") && samePathText(v, cur)) {
			return labels, i
		}
	}
	return labels, 0
}

func (g *gui) dirty() bool {
	switch g.sel.kind {
	case "new":
		return true
	case "server":
		if s := g.current(); s != nil && !g.formLocked {
			a, b := g.readForm(), formFor(s)
			if a.Key != b.Key && a.Key != "" && b.Key != "" && !strings.HasPrefix(a.Key, "new:") && samePathText(a.Key, b.Key) {
				a.Key = b.Key
			}
			return a != b
		}
	}
	return false
}

// ---------- state changes (call with g.mu held) ----------

func (g *gui) selectItem(next selection) {
	g.sel = next
	s := g.current()
	g.formLocked = s != nil && s.Kind == "config" && len(s.Extra) > 0
	switch next.kind {
	case "new":
		g.formLocked = false
		g.fillForm(formFor(nil))
	case "server":
		g.fillForm(formFor(s))
	}
	g.mainList.Position = layout.Position{}
}

// apply installs a freshly loaded state. selectAlias, when set, becomes the
// selection; refill resets the form to the saved values.
func (g *gui) apply(sv *StateView, selectAlias string, refill bool) {
	g.data = sv
	g.stamps = [2]int64{fileStamp(sv.ConfigPath), fileStamp(sv.KnownPath)}
	if selectAlias != "" {
		g.sel = selection{"server", selectAlias}
		refill = true
	}
	if g.sel.kind != "new" && g.current() == nil {
		g.sel = selection{}
		for _, kind := range []string{"managed", "config"} {
			for _, s := range sv.Servers {
				if s.Kind == kind && g.sel.kind == "" {
					g.sel = selection{"server", s.Alias}
				}
			}
		}
		refill = true
	}
	if refill {
		g.selectItem(g.sel)
	}
}

func (g *gui) toast(msg, tone string) {
	d := 3800 * time.Millisecond
	if tone == "bad" {
		d = 7 * time.Second
	}
	g.toasts = append(g.toasts, toast{msg, tone, time.Now().Add(d)})
	if len(g.toasts) > 3 {
		g.toasts = g.toasts[len(g.toasts)-3:]
	}
}

func (g *gui) copy(text, done string) {
	g.clip = text
	g.toast(done, "good")
}

// ---------- frame ----------

func (g *gui) frame(gtx C) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.clip != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(g.clip))})
		g.clip = ""
	}
	g.keys(gtx)
	g.winSize = gtx.Constraints.Max
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &g.ptrTag, Kinds: pointer.Move | pointer.Press | pointer.Drag})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			g.ptr = e.Position
		}
	}
	before := g.snapshot()
	defer func() {
		// Something changed while this frame was being drawn (a click, a
		// choice in a list): draw again so every part shows the new state.
		if g.snapshot() != before {
			gtx.Execute(op.InvalidateCmd{})
		}
	}()
	fillRect(gtx, image.Rectangle{Max: gtx.Constraints.Max}, colBg)

	base := gtx
	if g.modal != nil {
		base = gtx.Disabled()
	}
	layout.Flex{}.Layout(base,
		layout.Rigid(g.sidebar),
		layout.Flexed(1, g.mainPane),
	)
	if g.openSel != nil {
		// A click anywhere outside an open dropdown closes it. The dropdown's
		// own list is drawn later, so it stays on top of this.
		if g.selScrim.Clicked(gtx) {
			g.openSel.open = false
			g.openSel = nil
		}
		g.selScrim.Layout(gtx, func(gtx C) D { return D{Size: gtx.Constraints.Max} })
	}
	g.overlay(gtx)
	if g.modal != nil {
		g.modalUI(gtx)
	}
	// Watch the pointer everywhere without taking clicks away from anything.
	pass := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &g.ptrTag)
	area.Pop()
	pass.Pop()
}

// frameState is what decides how the window looks, apart from typed text.
type frameState struct {
	sel          selection
	modal        *modal
	openSel      *selectBox
	data         *StateView
	busy         bool
	status       string
	toasts       int
	fv           formVals
	other, known bool
	locked, clip bool
}

func (g *gui) snapshot() frameState {
	return frameState{g.sel, g.modal, g.openSel, g.data, g.busy, g.status, len(g.toasts), g.fv, g.showOther, g.showKnown, g.formLocked, g.clip != ""}
}

// keys handles Escape (close whatever is open) and the two shortcuts.
func (g *gui) keys(gtx C) {
	event.Op(gtx.Ops, g)
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: "N", Required: key.ModShortcut},
			key.Filter{Name: "F", Required: key.ModShortcut},
		)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch {
		case e.Name == key.NameEscape && g.openSel != nil:
			g.openSel.open = false
			g.openSel = nil
		case e.Name == key.NameEscape && g.modal != nil && g.modal.dismiss:
			g.answer("", false)
		case e.Name == "N" && g.modal == nil && !g.busy:
			g.onAdd()
		case e.Name == "F" && g.modal == nil:
			gtx.Execute(key.FocusCmd{Tag: &g.search})
		}
	}
}

// ---------- sidebar ----------

func (g *gui) sidebar(gtx C) D {
	w := gtx.Dp(304)
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	fillRect(gtx, image.Rectangle{Max: gtx.Constraints.Max}, colPanel)
	fillRect(gtx, image.Rect(w-gtx.Dp(1), 0, w, gtx.Constraints.Max.Y), colLine)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 18, Right: 18, Top: 20, Bottom: 16}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return g.logo(gtx, 38) }),
					gap(12),
					layout.Rigid(func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(g.txt(15, appName, colText, bold).Layout),
							layout.Rigid(g.txt(12, "Passwordless SSH for your AI apps", colMuted).Layout),
						)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 14, Right: 14, Bottom: 8}.Layout(gtx, func(gtx C) D {
				return g.input(gtx, &g.search, "Search servers", false, false, icSearch)
			})
		}),
		layout.Flexed(1, g.sideRows),
		layout.Rigid(g.sideFoot),
	)
}

func (g *gui) logo(gtx C, size unit.Dp) D {
	px := gtx.Dp(size)
	r := image.Rectangle{Max: image.Pt(px, px)}
	gradRRect(gtx, r, px*11/38, colAccent, colAcc2)
	gtx.Constraints = layout.Exact(r.Max)
	layout.Center.Layout(gtx, func(gtx C) D { return g.icon(gtx, icKey, size/2, rgb(0xffffff)) })
	return D{Size: r.Max}
}

func (g *gui) sideRows(gtx C) D {
	q := strings.ToLower(strings.TrimSpace(g.search.Text()))
	match := func(s *ServerView) bool {
		return q == "" || strings.Contains(strings.ToLower(s.Alias+" "+s.Host+" "+s.User), q)
	}
	by := func(kind string, filtered bool) []*ServerView {
		var out []*ServerView
		for i := range g.data.Servers {
			if s := &g.data.Servers[i]; s.Kind == kind && (!filtered || match(s)) {
				out = append(out, s)
			}
		}
		return out
	}
	var rows []layout.Widget
	section := func(title string, count int, toggle *bool, add bool) {
		rows = append(rows, func(gtx C) D {
			id := "sec:" + title
			c := g.clk(id)
			if toggle != nil && c.Clicked(gtx) {
				*toggle = !*toggle
			}
			inner := func(gtx C) D {
				return layout.Inset{Left: 10, Right: 6, Top: 14, Bottom: 6}.Layout(gtx, func(gtx C) D {
					kids := []layout.FlexChild{}
					if toggle != nil {
						ic := icChevR
						if *toggle {
							ic = icChevD
						}
						kids = append(kids, layout.Rigid(func(gtx C) D { return g.icon(gtx, ic, 14, colFaint) }), gap(4))
					}
					kids = append(kids,
						layout.Rigid(g.txt(11, strings.ToUpper(title), colFaint, bold).Layout), gap(6),
						layout.Rigid(func(gtx C) D {
							return box{bg: rgb(0x1b2029), radius: 6, in: layout.Inset{Left: 6, Right: 6, Top: 1, Bottom: 1}}.Layout(gtx,
								g.txt(10.5, fmt.Sprint(count), colMuted, bold).Layout)
						}),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
					)
					if add {
						ab := g.clk("add")
						if ab.Clicked(gtx) {
							g.onAdd()
						}
						kids = append(kids, layout.Rigid(func(gtx C) D {
							if g.busy {
								gtx = gtx.Disabled()
							}
							return g.button(gtx, ab, btnStyle{kind: btnGhost, small: true, icon: icPlus})
						}))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
				})
			}
			if toggle != nil {
				return c.Layout(gtx, inner)
			}
			return inner(gtx)
		})
	}
	item := func(s *ServerView) {
		rows = append(rows, func(gtx C) D {
			kind := "server"
			if s.Kind == "known" {
				kind = "known"
			}
			c := g.clk("row:" + kind + ":" + s.Alias)
			if c.Clicked(gtx) {
				g.selectItem(selection{kind, s.Alias})
			}
			selected := g.sel.kind == kind && g.sel.alias == s.Alias
			bg, border := color.NRGBA{}, color.NRGBA{}
			if selected {
				bg, border = rgb(0x1a2030), rgb(0x2a3450)
			} else if c.Hovered() {
				bg = rgb(0x161a21)
			}
			return layout.Inset{Bottom: 1}.Layout(gtx, func(gtx C) D {
				return c.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return box{bg: bg, border: border, radius: 9, in: layout.Inset{Left: 10, Right: 10, Top: 8, Bottom: 8}}.Layout(gtx, func(gtx C) D {
						sub := target(s)
						if s.Kind == "known" {
							sub = plural(len(s.HostKeys), "host key")
						}
						kids := []layout.FlexChild{
							layout.Rigid(func(gtx C) D { return g.avatar(gtx, s, 32) }),
							gap(11),
							layout.Flexed(1, func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(g.txt(14, s.Alias, colText, medium, oneLine).Layout),
									layout.Rigid(g.txt(12, sub, colMuted, monoFont, oneLine).Layout),
								)
							}),
						}
						if s.Kind != "known" {
							tone, _ := g.statusOf(s)
							kids = append(kids, gap(8), layout.Rigid(func(gtx C) D { return g.dot(gtx, tone) }))
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
					})
				})
			})
		})
	}
	note := func(msg string) {
		rows = append(rows, func(gtx C) D {
			return layout.Inset{Left: 12, Top: 8, Bottom: 8}.Layout(gtx, g.txt(12.5, msg, colFaint).Layout)
		})
	}

	managed := by("managed", true)
	section("Servers", len(by("managed", false)), nil, true)
	switch {
	case len(managed) > 0:
		for _, s := range managed {
			item(s)
		}
	case q != "":
		note("No matches")
	default:
		rows = append(rows, func(gtx C) D {
			c := g.clk("addrow")
			if c.Clicked(gtx) {
				g.onAdd()
			}
			return layout.Inset{Left: 2, Right: 2, Top: 4}.Layout(gtx, func(gtx C) D {
				return c.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					border := colLine2
					if c.Hovered() {
						border = colAccent
					}
					return box{border: border, radius: 10, in: layout.UniformInset(10)}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return g.icon(gtx, icPlus, 16, colMuted) }), gap(10),
							layout.Rigid(g.txt(14, "Add a server", colMuted).Layout))
					})
				})
			})
		})
	}
	if all := by("config", false); len(all) > 0 {
		section("Other config hosts", len(all), &g.showOther, false)
		if g.showOther {
			list := by("config", true)
			for _, s := range list {
				item(s)
			}
			if len(list) == 0 {
				note("No matches")
			}
		}
	}
	if all := by("known", false); len(all) > 0 {
		section("Unlinked host keys", len(all), &g.showKnown, false)
		if g.showKnown {
			list := by("known", true)
			for _, s := range list {
				item(s)
			}
			if len(list) == 0 {
				note("No matches")
			}
		}
	}
	return layout.Inset{Left: 8, Right: 8, Top: 2}.Layout(gtx, func(gtx C) D {
		ls := material.List(g.th, &g.sideList)
		ls.AnchorStrategy = material.Overlay
		ls.Indicator.Color = rgb(0x323a47)
		return ls.Layout(gtx, len(rows), func(gtx C, i int) D { return rows[i](gtx) })
	})
}

func (g *gui) sideFoot(gtx C) D {
	ai, cfg, fold, rel, don := g.clk("ai"), g.clk("openConfig"), g.clk("openFolder"), g.clk("reload"), g.clk("donate")
	src, terms := g.clk("source"), g.clk("terms")
	if src.Clicked(gtx) {
		if err := openURL(links["source"]); err != nil {
			g.try(err)
		} else {
			g.toast("Opened the source code page in your web browser", "info")
		}
	}
	if terms.Clicked(gtx) && g.modal == nil {
		go g.notice(false)
	}
	if ai.Clicked(gtx) {
		g.onCopyAI()
	}
	if cfg.Clicked(gtx) {
		g.try(g.app.openConfig())
	}
	if fold.Clicked(gtx) {
		g.try(openFolder(g.app.t.Dir))
	}
	if rel.Clicked(gtx) && !g.busy {
		g.onReload()
	}
	if don.Clicked(gtx) {
		g.onDonate()
	}
	var labels []string
	var paths []string
	cur := 0
	for i, f := range g.data.Folders {
		l := f.Label + ": " + configPathFor(f.Path)
		if !f.Exists {
			l += " (new)"
		}
		labels, paths = append(labels, l), append(paths, f.Path)
		if f.Active {
			cur = i
		}
	}
	labels, paths = append(labels, "Choose another folder…"), append(paths, "")

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(g.divider),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 12, Right: 12, Top: 12, Bottom: 12}.Layout(gtx, func(gtx C) D {
				kids := []layout.FlexChild{
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return g.button(gtx, ai, btnStyle{kind: btnAI, icon: icSpark, label: "Copy server list for AI", wide: true})
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 4, Top: 12, Bottom: 6}.Layout(gtx, g.txt(11, "KEYS AND CONFIG FOLDER", colFaint, bold).Layout)
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								if g.busy {
									gtx = gtx.Disabled()
								}
								d, picked := g.selectUI(gtx, &g.folderSel, labels, cur, true, g.busy, true)
								if picked >= 0 && picked != cur {
									g.onFolder(paths[picked])
								}
								return d
							}),
							gap(2),
							layout.Rigid(func(gtx C) D { return g.button(gtx, cfg, btnStyle{kind: btnGhost, small: true, icon: icFile}) }),
							layout.Rigid(func(gtx C) D { return g.button(gtx, fold, btnStyle{kind: btnGhost, small: true, icon: icFolder}) }),
							layout.Rigid(func(gtx C) D { return g.button(gtx, rel, btnStyle{kind: btnGhost, small: true, icon: icRefresh}) }),
						)
					}),
				}
				if g.data.LinkError != "" {
					kids = append(kids, layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 4, Top: 8}.Layout(gtx, g.txt(11.5, "ssh can't see this folder yet: "+g.data.LinkError, colWarn).Layout)
					}))
				}
				kids = append(kids,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 4, Right: 4, Top: 12}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(g.txt(11.5, "Made by ", colFaint).Layout),
								layout.Rigid(g.txt(11.5, "ITEAdvisors", colMuted, bold).Layout),
								layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
								layout.Rigid(func(gtx C) D { return g.link(gtx, don, icHeart, "Support this app") }),
							)
						})
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 4, Right: 4, Top: 8}.Layout(gtx, g.txt(10.5, privacyLine, colFaint).Layout)
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 4, Right: 4, Top: 8}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D { return g.link(gtx, src, icLink, "Source code") }),
								gap(14),
								layout.Rigid(func(gtx C) D { return g.link(gtx, terms, icInfo, "Disclaimer") }),
							)
						})
					}),
				)
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})
		}),
	)
}

// ---------- main pane ----------

func (g *gui) mainPane(gtx C) D {
	gtx.Constraints.Min = gtx.Constraints.Max
	s := g.current()
	if g.sel.kind == "" || (g.sel.kind != "new" && s == nil) {
		return layout.Center.Layout(gtx, g.hero)
	}
	view := g.serverView
	switch g.sel.kind {
	case "new":
		view = g.newView
	case "known":
		view = g.knownView
	}
	ls := material.List(g.th, &g.mainList)
	ls.AnchorStrategy = material.Overlay
	ls.Indicator.Color = rgb(0x323a47)
	return ls.Layout(gtx, 1, func(gtx C, _ int) D {
		padX, top, bottom := gtx.Dp(40), gtx.Dp(30), gtx.Dp(64)
		w := min(gtx.Constraints.Max.X-2*padX, gtx.Dp(820))
		left := (gtx.Constraints.Max.X - w) / 2
		st := op.Offset(image.Pt(left, top)).Push(gtx.Ops)
		cg := gtx
		cg.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, 1<<24)}
		d := view(cg)
		st.Pop()
		return D{Size: image.Pt(gtx.Constraints.Max.X, d.Size.Y+top+bottom)}
	})
}

// stack lays widgets out top to bottom with a gap between them.
func stack(gtx C, space unit.Dp, ws ...layout.Widget) D {
	kids := make([]layout.FlexChild, 0, len(ws)*2)
	for i, w := range ws {
		if w == nil {
			continue
		}
		if i > 0 && len(kids) > 0 {
			kids = append(kids, gap(space))
		}
		kids = append(kids, layout.Rigid(w))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (g *gui) howSteps() [][2]string {
	return [][2]string{
		{"Dedicated key", "A new key just for this server, stored in your .ssh folder."},
		{"Verify the server", "You confirm its fingerprint once. It's saved to known_hosts."},
		{"Install the key", "A terminal asks for the password one time. It's never stored."},
		{"Ready for AI", "The login is tested, then any app can run ssh name."},
	}
}

func (g *gui) howRow(gtx C, steps [][2]string) D {
	n := len(steps)
	gapPx := gtx.Dp(12)
	w := (gtx.Constraints.Max.X - gapPx*(n-1)) / n
	var kids []layout.FlexChild
	for i, s := range steps {
		if i > 0 {
			kids = append(kids, gap(12))
		}
		kids = append(kids, layout.Rigid(fixed(w, func(gtx C) D {
			return box{bg: colCard, border: colLine, radius: 12, in: layout.UniformInset(14)}.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						px := gtx.Dp(24)
						gtx.Constraints = layout.Exact(image.Pt(px, px))
						fillRRect(gtx, image.Rectangle{Max: image.Pt(px, px)}, gtx.Dp(7), alpha(colAccent, .14))
						return layout.Center.Layout(gtx, g.txt(12, fmt.Sprint(i+1), rgb(0xa9b7ff), bold).Layout)
					}),
					gap(10),
					layout.Rigid(g.txt(13, s[0], colText, bold).Layout),
					gap(3),
					layout.Rigid(g.txt(12.5, s[1], colMuted).Layout),
				)
			})
		})))
	}
	return layout.Flex{}.Layout(gtx, kids...)
}

func (g *gui) hero(gtx C) D {
	add := g.clk("heroAdd")
	if add.Clicked(gtx) {
		g.onAdd()
	}
	w := min(gtx.Constraints.Max.X-gtx.Dp(80), gtx.Dp(640))
	gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return g.logo(gtx, 64) }),
		gap(22),
		layout.Rigid(g.txt(28, "Give your AI apps SSH access", colText, bold, centered).Layout),
		gap(8),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(520))
			return g.txt(15, "Add a server once. Claude, ChatGPT and other tools on this computer can then run ssh name \"command\", "+
				"by name or by IP address, with no passwords or prompts.", colMuted, centered).Layout(gtx)
		}),
		gap(28),
		layout.Rigid(func(gtx C) D { return g.howRow(gtx, g.howSteps()[:3]) }),
		gap(28),
		layout.Rigid(func(gtx C) D {
			return g.button(gtx, add, btnStyle{kind: btnPrimary, icon: icPlus, label: "Add your first server"})
		}),
	)
}

func (g *gui) header(gtx C, lead layout.Widget, title string, pill layout.Widget, sub string, subMono bool, actions ...layout.Widget) D {
	kids := []layout.FlexChild{
		layout.Rigid(lead), gap(16),
		layout.Flexed(1, func(gtx C) D {
			subL := g.txt(13.5, sub, colMuted, oneLine)
			if subMono {
				subL = g.txt(13, sub, colMuted, monoFont, oneLine)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					row := []layout.FlexChild{layout.Rigid(g.txt(24, title, colText, bold, oneLine).Layout)}
					if pill != nil {
						row = append(row, gap(12), layout.Rigid(pill))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
				}),
				gap(3),
				layout.Rigid(subL.Layout),
			)
		}),
	}
	for _, a := range actions {
		kids = append(kids, gap(8), layout.Rigid(a))
	}
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D { return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...) })
}

func (g *gui) note(gtx C, warn bool, ic *widget.Icon, text string, chips []string) D {
	col, fg := colAccent, rgb(0xc5cff7)
	icCol := rgb(0x93a6ff)
	if warn {
		col, fg, icCol = colWarn, rgb(0xefd9ad), colWarn
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return box{bg: alpha(col, .07), border: alpha(col, .22), radius: 12, in: layout.Inset{Left: 15, Right: 15, Top: 13, Bottom: 13}}.Layout(gtx, func(gtx C) D {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return g.icon(gtx, ic, 16, icCol) }), gap(12),
			layout.Flexed(1, func(gtx C) D {
				kids := []layout.FlexChild{layout.Rigid(g.txt(13, text, fg).Layout)}
				if len(chips) > 0 {
					kids = append(kids, gap(8), layout.Rigid(func(gtx C) D {
						var row []layout.FlexChild
						for _, c := range chips {
							row = append(row, layout.Rigid(func(gtx C) D {
								return box{bg: color.NRGBA{R: 255, G: 255, B: 255, A: 15}, radius: 6, in: layout.Inset{Left: 8, Right: 8, Top: 2, Bottom: 2}}.Layout(gtx,
									g.txt(12, c, rgb(0xe2e6ee), monoFont).Layout)
							}), gap(6))
						}
						return layout.Flex{}.Layout(gtx, row...)
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			}),
		)
	})
}

func (g *gui) cardHead(gtx C, title, sub string, actions ...layout.Widget) D {
	return layout.Inset{Left: 18, Right: 18, Top: 14, Bottom: 14}.Layout(gtx, func(gtx C) D {
		kids := []layout.FlexChild{layout.Flexed(1, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(g.txt(13.5, title, colText, bold).Layout), gap(2),
				layout.Rigid(g.txt(12.5, sub, colMuted, oneLine).Layout))
		})}
		for _, a := range actions {
			kids = append(kids, gap(6), layout.Rigid(a))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
}

func (g *gui) stepRow(gtx C, tone, title, desc string, actions ...layout.Widget) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(g.divider),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 18, Right: 18, Top: 14, Bottom: 14}.Layout(gtx, func(gtx C) D {
				kids := []layout.FlexChild{
					layout.Rigid(func(gtx C) D { return g.stepMark(gtx, tone) }), gap(14),
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(g.txt(14, title, colText, bold, oneLine).Layout), gap(1),
							layout.Rigid(g.txt(12.5, desc, colMuted, oneLine).Layout))
					}),
				}
				for _, a := range actions {
					kids = append(kids, gap(6), layout.Rigid(a))
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
			})
		}),
	)
}

// act draws a button that is switched off while a flow is running.
func (g *gui) act(id string, s btnStyle, do func()) layout.Widget {
	return func(gtx C) D {
		c := g.clk(id)
		if c.Clicked(gtx) && !g.busy {
			do()
		}
		if g.busy {
			gtx = gtx.Disabled()
		}
		return g.button(gtx, c, s)
	}
}

func (g *gui) accessCard(gtx C, s *ServerView) D {
	a := s.Alias
	t := g.tests[a]
	rows := []layout.Widget{func(gtx C) D {
		return g.cardHead(gtx, "Access", "Everything an AI app needs to run ssh "+a+" unattended")
	}}
	// 1. the key on this computer
	switch {
	case s.IdentityFile == "":
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "muted", "Default keys", "No key file set, so ssh tries your default keys.")
		})
	case s.KeyExists:
		d := s.IdentityFile
		if s.KeyType != "" {
			d += "  ·  " + strings.ToUpper(s.KeyType)
		}
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "good", "Key file", d,
				g.act("copyPub", btnStyle{kind: btnGhost, small: true, icon: icCopy, label: "Copy public key"}, func() { g.onCopyPub(a) }))
		})
	default:
		var acts []layout.Widget
		if s.Kind == "managed" {
			acts = append(acts, g.act("fixKey", btnStyle{small: true, icon: icKey, label: "Create key"}, func() { g.saveFlow(formFor(s)) }))
		}
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "warn", "Key file is missing", s.IdentityFile+" wasn't found.", acts...)
		})
	}
	// 2. the server's identity
	if len(s.HostKeys) > 0 {
		k := byPref(s.HostKeys)[0]
		d := keyTypeLabel(k.Type) + "  " + k.Fp
		if n := len(s.HostKeys) - 1; n > 0 {
			d += fmt.Sprintf("  +%d more", n)
		}
		rows = append(rows, func(gtx C) D { return g.stepRow(gtx, "good", "Server identity verified", d) })
	} else {
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "warn", "Server identity not verified", "ssh will refuse to connect until you confirm the server's host key.",
				g.act("verify", btnStyle{kind: btnPrimary, small: true, icon: icShield, label: "Verify host key"}, g.onVerify))
		})
	}
	// 3. the login
	test := func(label string, kind btnKind) layout.Widget {
		return g.act("test2", btnStyle{kind: kind, small: true, icon: icZap, label: label}, g.onTest)
	}
	switch t.Kind {
	case "checking":
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "busy", "Checking passwordless login", "Running ssh "+a+" with prompts disabled")
		})
	case "ok":
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "good", "Passwordless login works", t.Message, test("Retest", btnGhost))
		})
	case "auth":
		d := "The server still asks for a password. Install the key once and you're done."
		acts := []layout.Widget{test("Retest", btnPlain)}
		switch {
		case s.IdentityFile == "":
			d = "Manage this server with the app to give it its own key."
		case s.KeyExists && s.Device == devManual:
			acts = append(acts, g.act("install", btnStyle{kind: btnPrimary, small: true, icon: icKey, label: "Show public key"}, g.onInstall))
		case s.KeyExists:
			acts = []layout.Widget{g.act("install", btnStyle{kind: btnPrimary, small: true, icon: icTerm, label: "Install key"}, g.onInstall)}
		}
		rows = append(rows, func(gtx C) D { return g.stepRow(gtx, "bad", "Key not installed on the server", d, acts...) })
	case "hostkey":
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "bad", "Host key rejected", t.Message,
				g.act("verify", btnStyle{kind: btnPrimary, small: true, icon: icShield, label: "Verify host key"}, g.onVerify))
		})
	case "algo":
		acts := []layout.Widget{test("Retest", btnPlain)}
		if s.Kind == "managed" {
			acts = []layout.Widget{g.act("legacy", btnStyle{kind: btnPrimary, small: true, icon: icShield, label: "Allow older encryption"}, g.onLegacy)}
		}
		rows = append(rows, func(gtx C) D { return g.stepRow(gtx, "bad", "Needs older encryption", t.Message, acts...) })
	case "error":
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "bad", "Couldn't connect", t.Message, test("Retry", btnPlain))
		})
	default:
		rows = append(rows, func(gtx C) D {
			return g.stepRow(gtx, "muted", "Passwordless login", "Not tested yet.", test("Test", btnPlain))
		})
	}
	return g.card(gtx, func(gtx C) D { return stack(gtx, 0, rows...) })
}

func (g *gui) field(gtx C, label string, w layout.Widget, help string) D {
	kids := []layout.FlexChild{layout.Rigid(g.txt(12, label, colMuted, medium).Layout), gap(6), layout.Rigid(w)}
	if help != "" {
		kids = append(kids, gap(6), layout.Rigid(g.txt(11.5, help, colFaint).Layout))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (g *gui) formUI(gtx C, isNew bool) D {
	for _, e := range []*widget.Editor{&g.fName, &g.fHost, &g.fPort, &g.fUser} {
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, submit := ev.(widget.SubmitEvent); submit && !g.busy && !g.formLocked && g.dirty() {
				g.saveFlow(g.readForm())
			}
		}
	}
	dis := g.formLocked
	gapPx := gtx.Dp(14)
	unitW := float32(gtx.Constraints.Max.X-2*gapPx) / 2.95
	c1, c3 := int(unitW*1.2), int(unitW*.55)
	c2 := gtx.Constraints.Max.X - 2*gapPx - c1 - c3
	span := c2 + gapPx + c3
	alias := strings.TrimSpace(g.fName.Text())
	if alias == "" {
		alias = "name"
	}
	userHelp := "The account on the server."
	if strings.TrimSpace(g.fUser.Text()) == "" && g.data.LocalUser != "" {
		userHelp = "Optional. Empty means your Windows name (" + g.data.LocalUser + ")."
	}
	keyHelp := "The private key used to log in."
	if isNew {
		keyHelp = "A dedicated key per server is easiest to revoke later."
	}
	keyLabels, keyCur := g.keyChoices()
	devLabels := make([]string, len(devices))
	devCur, devHelp := 0, devices[0].help
	for i, d := range devices {
		devLabels[i] = d.label
		if d.id == normDevice(g.fv.Device) {
			devCur, devHelp = i, d.help
		}
	}
	legCur := 0
	if g.fv.Legacy {
		legCur = 1
	}
	row := func(ws ...layout.Widget) layout.Widget {
		return func(gtx C) D {
			var kids []layout.FlexChild
			for i, w := range ws {
				if i > 0 {
					kids = append(kids, gap(14))
				}
				kids = append(kids, layout.Rigid(w))
			}
			return layout.Flex{}.Layout(gtx, kids...)
		}
	}
	return stack(gtx, 14,
		row(
			fixed(c1, func(gtx C) D {
				return g.field(gtx, "Name", func(gtx C) D { return g.input(gtx, &g.fName, "web", false, dis, nil) }, "What you and the apps type: ssh "+alias)
			}),
			fixed(c2, func(gtx C) D {
				return g.field(gtx, "IP address or hostname", func(gtx C) D { return g.input(gtx, &g.fHost, "203.0.113.10", true, dis, nil) }, "")
			}),
			fixed(c3, func(gtx C) D {
				return g.field(gtx, "Port", func(gtx C) D { return g.input(gtx, &g.fPort, "22", true, dis, nil) }, "")
			}),
		),
		row(
			fixed(c1, func(gtx C) D {
				return g.field(gtx, "Username", func(gtx C) D { return g.input(gtx, &g.fUser, "root", false, dis, nil) }, userHelp)
			}),
			fixed(span, func(gtx C) D {
				return g.field(gtx, "Key file", func(gtx C) D {
					d, picked := g.selectUI(gtx, &g.fKey, keyLabels, keyCur, true, dis, false)
					if picked >= 0 {
						g.fv.Key = g.keyValues[picked]
					}
					return d
				}, keyHelp)
			}),
		),
		row(
			fixed(c1, func(gtx C) D {
				return g.field(gtx, "Device type", func(gtx C) D {
					d, picked := g.selectUI(gtx, &g.fDevice, devLabels, devCur, false, dis, false)
					if picked >= 0 {
						f := g.readForm()
						f.Device = devices[picked].id
						g.applyDevice(&f)
						g.fillForm(f)
					}
					return d
				}, devHelp)
			}),
			fixed(span, func(gtx C) D {
				return g.field(gtx, "Encryption", func(gtx C) D {
					d, picked := g.selectUI(gtx, &g.fLegacy, []string{"Standard (recommended)", "Also allow older encryption"}, legCur, false, dis, false)
					if picked >= 0 {
						f := g.readForm()
						f.Legacy = picked == 1
						g.applyLegacy(&f)
						g.fillForm(f)
					}
					return d
				}, "Turn on older encryption for old switches, firewalls, storage boxes or old ESXi that ssh otherwise refuses to talk to.")
			}),
		),
	)
}

func (g *gui) formFoot(gtx C) D {
	s := g.current()
	msg := ""
	var btns []layout.Widget
	switch {
	case g.sel.kind == "new":
		btns = append(btns,
			g.act("cancelNew", btnStyle{kind: btnGhost, label: "Cancel"}, g.onDiscard),
			g.act("save", btnStyle{kind: btnPrimary, icon: icPlus, label: "Add server"}, func() { g.saveFlow(g.readForm()) }))
	case s == nil:
		return D{}
	case g.formLocked:
		msg = "Edit this entry in the config file."
		btns = append(btns, g.act("openCfg2", btnStyle{icon: icFile, label: "Open config file"}, func() { g.try(g.app.openConfig()) }))
	case s.Kind == "config":
		msg = "Moves this entry into the app's section of your config and gives it a key."
		btns = append(btns, g.act("save", btnStyle{kind: btnPrimary, label: "Manage with this app"}, func() { g.saveFlow(g.readForm()) }))
	default:
		d := g.dirty()
		if d {
			msg = "Unsaved changes"
		}
		off := func(w layout.Widget) layout.Widget {
			return func(gtx C) D {
				if !d {
					gtx = gtx.Disabled()
				}
				return w(gtx)
			}
		}
		btns = append(btns,
			off(g.act("discard", btnStyle{kind: btnGhost, label: "Discard"}, g.onDiscard)),
			off(g.act("save", btnStyle{kind: btnPrimary, label: "Save changes"}, func() { g.saveFlow(g.readForm()) })))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(g.divider),
		layout.Rigid(func(gtx C) D {
			return layout.Background{}.Layout(gtx, func(gtx C) D {
				// rounded only at the bottom, to sit inside the card
				r := image.Rectangle{Max: gtx.Constraints.Min}
				defer clip.RRect{Rect: r, SE: gtx.Dp(14), SW: gtx.Dp(14)}.Push(gtx.Ops).Pop()
				fillRect(gtx, r, rgb(0x12161c))
				return D{Size: r.Max}
			}, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 18, Right: 18, Top: 12, Bottom: 12}.Layout(gtx, func(gtx C) D {
					kids := []layout.FlexChild{layout.Flexed(1, g.txt(12.5, msg, colFaint, oneLine).Layout)}
					for _, b := range btns {
						kids = append(kids, gap(8), layout.Rigid(b))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
				})
			})
		}),
	)
}

func (g *gui) connCard(gtx C, title, sub string, isNew bool) D {
	return g.card(gtx, func(gtx C) D {
		return stack(gtx, 0,
			func(gtx C) D {
				if title == "" {
					return D{Size: image.Pt(0, gtx.Dp(14))}
				}
				return g.cardHead(gtx, title, sub)
			},
			func(gtx C) D {
				return layout.Inset{Left: 18, Right: 18, Top: 4, Bottom: 18}.Layout(gtx, func(gtx C) D { return g.formUI(gtx, isNew) })
			},
			g.formFoot,
		)
	})
}

func (g *gui) hostKeysCard(gtx C, s *ServerView) D {
	ref := s.Kind + ":" + s.Alias
	acts := []layout.Widget{g.act("rescan", btnStyle{small: true, icon: icRefresh, label: "Re-scan"}, g.onRescan)}
	if len(s.HostKeys) > 0 {
		label := "Remove"
		if len(s.HostKeys) > 1 {
			label = "Remove all"
		}
		acts = append(acts, g.act("removeAll", btnStyle{kind: btnDanger, small: true, icon: icTrash, label: label}, g.onRemoveAll))
	}
	rows := []layout.Widget{func(gtx C) D {
		return g.cardHead(gtx, "Trusted host keys", "known_hosts entry "+s.KnownName, acts...)
	}}
	for _, k := range byPref(s.HostKeys) {
		rows = append(rows, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(g.divider),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 18, Right: 18, Top: 10, Bottom: 10}.Layout(gtx, func(gtx C) D {
						cp := g.clk("fp:" + ref + ":" + k.Fp)
						if cp.Clicked(gtx) {
							g.copy(k.Fp, "Fingerprint copied")
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return g.badge(gtx, keyTypeLabel(k.Type), 70) }), gap(14),
							layout.Flexed(1, g.txt(12.5, k.Fp, rgb(0xcfd5df), monoFont, oneLine).Layout),
							layout.Rigid(func(gtx C) D { return g.button(gtx, cp, btnStyle{kind: btnGhost, small: true, icon: icCopy}) }),
							layout.Rigid(g.act("rm:"+ref+":"+k.Fp, btnStyle{kind: btnGhost, small: true, icon: icTrash}, func() { g.onRemoveOne(k.Fp) })),
						)
					})
				}),
			)
		})
	}
	if len(s.HostKeys) == 0 {
		rows = append(rows, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(g.divider),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 18, Right: 18, Top: 14, Bottom: 18}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return g.icon(gtx, icShield, 16, colMuted) }), gap(10),
							layout.Rigid(g.txt(13, "No trusted host key for "+s.KnownName, colMuted).Layout))
					})
				}))
		})
	}
	return g.card(gtx, func(gtx C) D { return stack(gtx, 0, rows...) })
}

func (g *gui) serverView(gtx C) D {
	s := g.current()
	tone, label := g.statusOf(s)
	var lead layout.Widget
	switch tone {
	case "good":
		label = "Ready for AI apps"
		lead = func(gtx C) D { return g.icon(gtx, icCheck, 13, colGood) }
	case "busy":
		lead = func(gtx C) D { return g.spinner(gtx, 11) }
	}
	actions := []layout.Widget{
		g.act("terminal", btnStyle{icon: icTerm, label: "Open terminal"}, g.onTerminal),
		g.act("test", btnStyle{icon: icZap, label: "Test connection"}, g.onTest),
	}
	if s.Kind == "managed" {
		actions = append(actions, g.act("delete", btnStyle{kind: btnDanger, icon: icTrash}, g.onDelete))
	}
	cp := g.clk("copyCmd")
	if cp.Clicked(gtx) {
		g.copy("ssh "+s.Alias, "Copied")
	}
	hint := "Tell your AI app to use this"
	if s.Kind == "managed" && s.Host != s.Alias {
		hint = "or  ssh " + s.Host
	}
	var note layout.Widget
	if s.Kind == "config" {
		if g.formLocked {
			note = func(gtx C) D {
				return g.note(gtx, true, icInfo, "Not managed by this app. This entry uses settings the app doesn't edit, so it's read-only here. "+
					"You can still test it and manage its host key.", uniq(s.Extra))
			}
		} else {
			note = func(gtx C) D {
				return g.note(gtx, false, icInfo, "Not managed by this app. This host is in your SSH config but outside the app's section. "+
					"Choose Manage with this app below to give it its own key and keep it in sync.", nil)
			}
		}
	}
	connSub := "From your SSH config"
	if s.Kind == "managed" {
		connSub = "Saved in the app's section of your SSH config"
	}
	return stack(gtx, 16,
		func(gtx C) D {
			return g.header(gtx, func(gtx C) D { return g.avatar(gtx, s, 54) }, s.Alias,
				func(gtx C) D { return g.pill(gtx, tone, label, lead) }, target(s), true, actions...)
		},
		func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return box{bg: colInset, border: colLine, radius: 12, in: layout.Inset{Left: 16, Right: 8, Top: 9, Bottom: 9}}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(g.txt(15, "$", colGood, monoFont, bold).Layout), gap(12),
					layout.Rigid(g.txt(15, "ssh "+s.Alias, rgb(0xdfe4ee), monoFont, oneLine).Layout),
					layout.Rigid(g.txt(15, ` "command"`, colFaint, monoFont, oneLine).Layout),
					layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
					layout.Rigid(g.txt(12, hint, colFaint, oneLine).Layout), gap(12),
					layout.Rigid(func(gtx C) D { return g.button(gtx, cp, btnStyle{small: true, icon: icCopy, label: "Copy"}) }),
				)
			})
		},
		note,
		func(gtx C) D { return g.accessCard(gtx, s) },
		func(gtx C) D { return g.connCard(gtx, "Connection", connSub, false) },
		func(gtx C) D { return g.hostKeysCard(gtx, s) },
	)
}

func (g *gui) knownView(gtx C) D {
	s := g.current()
	return stack(gtx, 16,
		func(gtx C) D {
			return g.header(gtx, func(gtx C) D { return g.avatar(gtx, s, 54) }, s.Alias,
				func(gtx C) D { return g.pill(gtx, "muted", "Not in your SSH config", nil) },
				plural(len(s.HostKeys), "trusted host key"), false,
				g.act("fromKnown", btnStyle{kind: btnPrimary, icon: icPlus, label: "Add as server"}, g.onAddKnown))
		},
		func(gtx C) D {
			return g.note(gtx, false, icLink, "This host is in known_hosts, but no server in your SSH config points at it. "+
				"Add it as a server so AI apps can reach it by name, or remove the host key if you no longer use it.", nil)
		},
		func(gtx C) D { return g.hostKeysCard(gtx, s) },
	)
}

func (g *gui) newView(gtx C) D {
	return stack(gtx, 16,
		func(gtx C) D {
			return g.header(gtx, func(gtx C) D {
				px := gtx.Dp(54)
				r := image.Rectangle{Max: image.Pt(px, px)}
				fillRRect(gtx, r, gtx.Dp(15), rgb(0x1d2330))
				strokeRRect(gtx, r, gtx.Dp(15), float32(gtx.Dp(1)), colLine2)
				gtx.Constraints = layout.Exact(r.Max)
				layout.Center.Layout(gtx, func(gtx C) D { return g.icon(gtx, icPlus, 24, rgb(0xa9b7ff)) })
				return D{Size: r.Max}
			}, "Add a server", nil, "Saved to your SSH config so you and your AI apps can run ssh name", false)
		},
		func(gtx C) D { return g.connCard(gtx, "", "", true) },
		func(gtx C) D {
			return layout.Inset{Top: 10}.Layout(gtx, g.txt(11, "WHAT HAPPENS NEXT", colFaint, bold).Layout)
		},
		func(gtx C) D { return g.howRow(gtx, g.howSteps()) },
	)
}

// ---------- overlays: progress, toasts ----------

func (g *gui) overlay(gtx C) {
	now := gtx.Now
	// toasts, bottom right
	live := g.toasts[:0]
	for _, t := range g.toasts {
		if t.until.After(now) {
			live = append(live, t)
		}
	}
	g.toasts = live
	if len(g.toasts) > 0 {
		gtx.Execute(op.InvalidateCmd{At: g.toasts[0].until})
		layout.SE.Layout(gtx, func(gtx C) D {
			return layout.UniformInset(20).Layout(gtx, func(gtx C) D {
				var kids []layout.FlexChild
				for _, t := range g.toasts {
					kids = append(kids, layout.Rigid(func(gtx C) D {
						gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(420))
						gtx.Constraints.Min.X = 0
						ic, col := icCheck, colGood
						switch t.tone {
						case "bad":
							ic, col = icAlert, colBad
						case "info":
							ic, col = icInfo, colSoft
						}
						return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
							return box{bg: colToast, border: colLine2, radius: 11, in: layout.Inset{Left: 14, Right: 14, Top: 11, Bottom: 11}}.Layout(gtx, func(gtx C) D {
								return layout.Flex{}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return g.icon(gtx, ic, 16, col) }), gap(10),
									layout.Flexed(1, func(gtx C) D {
										gtx.Constraints.Min.X = 0
										return g.txt(13, t.msg, colText).Layout(gtx)
									}),
								)
							})
						})
					}))
				}
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx, kids...)
			})
		})
	}
	// what the running flow is doing, bottom centre
	if g.status != "" && g.modal == nil {
		layout.S.Layout(gtx, func(gtx C) D {
			return layout.Inset{Bottom: 22}.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(620))
				gtx.Constraints.Min.X = 0
				return box{bg: colToast, border: colLine2, radius: 20, in: layout.Inset{Left: 12, Right: 16, Top: 9, Bottom: 9}}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return g.spinner(gtx, 16) }), gap(10),
						layout.Rigid(g.txt(13, g.status, rgb(0xdfe4ee)).Layout),
					)
				})
			})
		})
	}
}

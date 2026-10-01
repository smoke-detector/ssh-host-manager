package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type App struct {
	t        *Tools
	mu       sync.Mutex // serialises writes to config / known_hosts
	cfgStamp int64
	pending  map[string][]string // scanned key lines awaiting user confirmation
	linkErr  error               // why ssh's own config couldn't be pointed at the app's folder
}

func newApp(t *Tools) *App {
	a := &App{t: t, pending: map[string][]string{}}
	a.linkErr = syncLink(t.defaultConfig(), t.Config, t.linked())
	return a
}

type ServerView struct {
	Kind         string    `json:"kind"` // managed | config | known
	Alias        string    `json:"alias"`
	Host         string    `json:"host"`
	Port         string    `json:"port"`
	User         string    `json:"user"`
	IdentityFile string    `json:"identityFile"`
	KeyPath      string    `json:"keyPath"`
	KeyExists    bool      `json:"keyExists"`
	KeyType      string    `json:"keyType"`
	Device       string    `json:"device"`
	Legacy       bool      `json:"legacy"`
	Extra        []string  `json:"extra"`
	HostKeys     []HostKey `json:"hostKeys"`
	KnownName    string    `json:"knownName"`
}

type KeyFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type StateView struct {
	Servers    []ServerView `json:"servers"`
	Keys       []KeyFile    `json:"keys"`
	Hashed     int          `json:"hashed"`
	SSHDir     string       `json:"sshDir"`
	ConfigPath string       `json:"configPath"`
	KnownPath  string       `json:"knownPath"`
	Home       string       `json:"home"`
	LocalUser  string       `json:"localUser"`
	Folders    []Folder     `json:"folders"`
	Linked     bool         `json:"linked"`
	LinkError  string       `json:"linkError"`
}

// dedupeHosts keeps the first entry per alias (ssh uses the first match),
// with managed entries first.
func dedupeHosts(all []*Host) []*Host {
	var out []*Host
	seen := map[string]bool{}
	for _, pass := range []bool{true, false} {
		for _, h := range all {
			if h.Managed != pass || seen[h.Alias] {
				continue
			}
			seen[h.Alias] = true
			out = append(out, h)
		}
	}
	return out
}

func (a *App) hostKeysFor(kh *knownHosts, host, port string) []HostKey {
	if e := kh.Map[khKey(host, port)]; e != nil {
		return e.Keys
	}
	if kh.Hashed > 0 {
		return a.t.lookupHostKeys(knownName(host, port))
	}
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func (a *App) state() (*StateView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.t.linked() {
		a.linkErr = syncLink(a.t.defaultConfig(), a.t.Config, true)
	}
	cf, err := readConfig(a.t.Config)
	if err != nil {
		return nil, err
	}
	a.cfgStamp = cf.Stamp
	kh := readKnownHosts(a.t.Known)
	hosts := dedupeHosts(parseHosts(cf))

	sv := &StateView{Hashed: kh.Hashed, SSHDir: a.t.Dir, ConfigPath: a.t.Config, KnownPath: a.t.Known, Home: homeDir(), LocalUser: localUser(),
		Folders: a.t.folders(), Linked: a.t.linked()}
	if a.linkErr != nil {
		sv.LinkError = a.linkErr.Error()
	}
	used := map[string]bool{}
	usedFp := map[string]bool{}
	for _, h := range hosts {
		kind := "config"
		if h.Managed {
			kind = "managed"
		}
		v := ServerView{Kind: kind, Alias: h.Alias, Host: h.HostName, Port: h.Port, User: h.User,
			IdentityFile: h.IdentityFile, Extra: h.Extra, KnownName: knownName(h.HostName, h.Port),
			Device: normDevice(h.Device), Legacy: h.Legacy}
		if h.IdentityFile != "" {
			v.KeyPath = expandHome(h.IdentityFile)
			v.KeyExists = exists(v.KeyPath) && exists(v.KeyPath+".pub")
			v.KeyType = keyKind(v.KeyPath + ".pub")
		}
		v.HostKeys = a.hostKeysFor(kh, h.HostName, h.Port)
		if v.HostKeys == nil {
			v.HostKeys = []HostKey{}
		}
		if v.Extra == nil {
			v.Extra = []string{}
		}
		used[khKey(h.HostName, h.Port)] = true
		for _, k := range v.HostKeys {
			usedFp[k.Fp+"|"+h.Port] = true
		}
		sv.Servers = append(sv.Servers, v)
	}

	// Hosts in known_hosts that no config entry points at. Names whose keys
	// all belong to a config server (a second name on the same line) are the
	// same machine and aren't listed again.
	var known []ServerView
	for _, k := range kh.Order {
		if used[k] {
			continue
		}
		e := kh.Map[k]
		fresh := false
		for _, hk := range e.Keys {
			if !usedFp[hk.Fp+"|"+e.Port] {
				fresh = true
			}
		}
		if !fresh {
			continue
		}
		known = append(known, ServerView{Kind: "known", Alias: knownName(e.Host, e.Port), Host: e.Host, Port: e.Port,
			HostKeys: e.Keys, Extra: []string{}, KnownName: knownName(e.Host, e.Port)})
	}
	sort.SliceStable(known, func(i, j int) bool { return strings.ToLower(known[i].Alias) < strings.ToLower(known[j].Alias) })
	sv.Servers = append(sv.Servers, known...)

	if ents, err := os.ReadDir(a.t.Dir); err == nil {
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || strings.HasSuffix(n, ".pub") || !exists(filepath.Join(a.t.Dir, n+".pub")) {
				continue
			}
			sv.Keys = append(sv.Keys, KeyFile{Path: configPathFor(filepath.Join(a.t.Dir, n)), Name: n,
				Type: keyKind(filepath.Join(a.t.Dir, n+".pub"))})
		}
	}
	if sv.Servers == nil {
		sv.Servers = []ServerView{}
	}
	if sv.Keys == nil {
		sv.Keys = []KeyFile{}
	}
	return sv, nil
}

var (
	aliasRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_-]*$`)
	userRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@-]*$`)
)

type SaveReq struct {
	Original     string `json:"original"`
	Alias        string `json:"alias"`
	Host         string `json:"host"`
	Port         string `json:"port"`
	User         string `json:"user"`
	IdentityFile string `json:"identityFile"` // a path, "new:ed25519", "new:rsa", or "" for the device's default
	Device       string `json:"device"`
	Legacy       bool   `json:"legacy"`
}

type SaveRes struct {
	Alias          string `json:"alias"`
	CreatedKey     string `json:"createdKey"`
	RemovedHostKey string `json:"removedHostKey"`
}

func (a *App) save(r SaveReq) (*SaveRes, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r.Alias, r.Host, r.User = strings.TrimSpace(r.Alias), strings.TrimSpace(r.Host), strings.TrimSpace(r.User)
	r.Port, r.IdentityFile = strings.TrimSpace(r.Port), strings.Trim(strings.TrimSpace(r.IdentityFile), `"`)
	if r.Port == "" {
		r.Port = "22"
	}
	switch {
	case !aliasRe.MatchString(r.Alias):
		return nil, errors.New("Name: use letters, digits, dot, dash or underscore (no spaces)")
	case !hostRe.MatchString(r.Host):
		return nil, errors.New("Enter a valid IP address or hostname")
	case r.User != "" && !userRe.MatchString(r.User):
		return nil, errors.New("Enter a valid username")
	case strings.Contains(r.IdentityFile, `"`):
		return nil, errors.New("Key file path can't contain quotes")
	}
	if p, err := strconv.Atoi(r.Port); err != nil || p < 1 || p > 65535 {
		return nil, errors.New("Port must be a number from 1 to 65535")
	} else {
		r.Port = strconv.Itoa(p)
	}

	cf, err := readConfig(a.t.Config)
	if err != nil {
		return nil, err
	}
	if cf.Stamp != a.cfgStamp {
		return nil, errStale
	}
	all := parseHosts(cf)
	hosts := dedupeHosts(all)
	var orig *Host
	if r.Original != "" {
		for _, h := range hosts {
			if h.Alias == r.Original {
				orig = h
			}
		}
		if orig == nil {
			return nil, fmt.Errorf("%q is no longer in your config", r.Original)
		}
	}
	for _, h := range hosts {
		if h.Alias == r.Alias && h != orig {
			return nil, fmt.Errorf("there is already a server named %q", r.Alias)
		}
	}
	drop := map[int]bool{}
	if orig != nil && !orig.Managed {
		if len(orig.Extra) > 0 {
			return nil, fmt.Errorf("%q uses settings this app doesn't manage (%s); edit it in the config file instead", orig.Alias, strings.Join(uniq(orig.Extra), ", "))
		}
		for n := orig.Start; n <= orig.End; n++ {
			drop[n] = true
		}
		// take the blank separator line with it, so no double gap is left behind
		if nx := orig.End + 1; nx < len(cf.Lines) && strings.TrimSpace(cf.Lines[nx]) == "" {
			drop[nx] = true
		}
	}

	r.Device = normDevice(r.Device)
	kind := defaultKeyKind(r.Device)
	if k, ok := strings.CutPrefix(r.IdentityFile, "new:"); ok {
		if k != "ed25519" && k != "rsa" {
			return nil, errors.New("unknown key type")
		}
		kind, r.IdentityFile = k, ""
	}
	keyPath := filepath.Join(a.t.Dir, "id_"+kind+"_"+r.Alias)
	if r.IdentityFile != "" {
		keyPath = expandHome(r.IdentityFile)
	}
	res := &SaveRes{Alias: r.Alias}
	created, err := a.t.ensureKey(keyPath, r.Alias, kind)
	if err != nil {
		return nil, err
	}
	if created {
		res.CreatedKey = configPathFor(keyPath)
	}

	nh := &Host{Alias: r.Alias, HostName: r.Host, User: r.User, Port: r.Port, IdentityFile: keyPath,
		Device: r.Device, Legacy: r.Legacy}
	var managed []*Host
	placed := false
	for _, h := range all {
		if !h.Managed {
			continue
		}
		if h == orig {
			managed = append(managed, nh)
			placed = true
			continue
		}
		managed = append(managed, h)
	}
	if !placed {
		managed = append(managed, nh)
	}
	if err := writeConfig(a.t.Config, managed, drop, a.cfgStamp, a.t.blockOpts()); err != nil {
		return nil, err
	}
	a.t.secure(a.t.Config)
	a.cfgStamp = fileStamp(a.t.Config)

	// Moved to a new address: drop the old host key unless something else uses it.
	if orig != nil && (orig.HostName != r.Host || orig.Port != r.Port) {
		still := false
		for _, h := range hosts {
			if h != orig && strings.EqualFold(h.HostName, orig.HostName) && h.Port == orig.Port {
				still = true
			}
		}
		if !still {
			name := knownName(orig.HostName, orig.Port)
			if err := a.t.removeHostKey(name); err == nil {
				res.RemovedHostKey = name
			}
		}
	}
	return res, nil
}

type DeleteReq struct {
	Alias     string `json:"alias"`
	DeleteKey bool   `json:"deleteKey"`
}

type DeleteRes struct {
	RemovedHostKey string `json:"removedHostKey"`
	KeptHostKeyFor string `json:"keptHostKeyFor"`
	DeletedKey     string `json:"deletedKey"`
}

func (a *App) del(r DeleteReq) (*DeleteRes, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cf, err := readConfig(a.t.Config)
	if err != nil {
		return nil, err
	}
	if cf.Stamp != a.cfgStamp {
		return nil, errStale
	}
	all := parseHosts(cf)
	var target *Host
	for _, h := range dedupeHosts(all) {
		if h.Alias == r.Alias && h.Managed {
			target = h
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%q is not a server managed by this app", r.Alias)
	}
	var managed []*Host
	for _, h := range all {
		if h.Managed && h != target {
			managed = append(managed, h)
		}
	}
	if err := writeConfig(a.t.Config, managed, nil, a.cfgStamp, a.t.blockOpts()); err != nil {
		return nil, err
	}
	a.t.secure(a.t.Config)
	a.cfgStamp = fileStamp(a.t.Config)

	res := &DeleteRes{}
	var others []*Host
	for _, h := range all {
		if h != target {
			others = append(others, h)
		}
	}
	for _, h := range others {
		if strings.EqualFold(h.HostName, target.HostName) && h.Port == target.Port {
			res.KeptHostKeyFor = h.Alias
		}
	}
	if res.KeptHostKeyFor == "" {
		name := knownName(target.HostName, target.Port)
		if err := a.t.removeHostKey(name); err == nil {
			res.RemovedHostKey = name
		}
	}
	if r.DeleteKey && target.IdentityFile != "" {
		kp := expandHome(target.IdentityFile)
		shared := false
		for _, h := range others {
			if h.IdentityFile != "" && filepath.Clean(expandHome(h.IdentityFile)) == filepath.Clean(kp) {
				shared = true
			}
		}
		if !shared {
			os.Remove(kp)
			os.Remove(kp + ".pub")
			res.DeletedKey = configPathFor(kp)
		}
	}
	return res, nil
}

type HostReq struct {
	Host string `json:"host"`
	Port string `json:"port"`
	Fp   string `json:"fp"` // removeHostKey only: remove just the key with this fingerprint
}

type OfferedKey struct {
	Type  string `json:"type"`
	Fp    string `json:"fp"`
	IsNew bool   `json:"isNew"`
}

type ScanRes struct {
	Status  string       `json:"status"` // unreachable | unchanged | new | added | changed
	Name    string       `json:"name"`
	Offered []OfferedKey `json:"offered"`
	Message string       `json:"message"`
}

func (a *App) scan(r HostReq) (*ScanRes, error) {
	if !hostRe.MatchString(r.Host) {
		return nil, errors.New("invalid host")
	}
	if r.Port == "" {
		r.Port = "22"
	}
	name := knownName(r.Host, r.Port)
	lines, offered, msg := a.t.keyscan(r.Host, r.Port)
	res := &ScanRes{Name: name, Offered: []OfferedKey{}}
	if len(offered) == 0 {
		res.Status, res.Message = "unreachable", msg
		return res, nil
	}
	trusted := map[string]bool{}
	for _, k := range a.hostKeysFor(readKnownHosts(a.t.Known), r.Host, r.Port) {
		trusted[k.Fp] = true
	}
	matched, fresh := 0, 0
	for _, k := range offered {
		isNew := !trusted[k.Fp]
		if isNew {
			fresh++
		} else {
			matched++
		}
		res.Offered = append(res.Offered, OfferedKey{k.Type, k.Fp, isNew})
	}
	switch {
	case fresh == 0:
		res.Status = "unchanged"
	case len(trusted) == 0:
		res.Status = "new"
	case matched > 0:
		res.Status = "added"
	default:
		res.Status = "changed"
	}
	a.mu.Lock()
	a.pending[khKey(r.Host, r.Port)] = lines
	a.mu.Unlock()
	return res, nil
}

func (a *App) trust(r HostReq) error {
	if r.Port == "" {
		r.Port = "22"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	lines := a.pending[khKey(r.Host, r.Port)]
	if len(lines) == 0 {
		return errors.New("scan the host key again before trusting it")
	}
	delete(a.pending, khKey(r.Host, r.Port))
	if err := a.t.removeHostKey(knownName(r.Host, r.Port)); err != nil {
		return err
	}
	return appendKnownHosts(a.t.Known, lines)
}

func (a *App) removeHostKey(r HostReq) error {
	if r.Port == "" {
		r.Port = "22"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Fp != "" {
		ok, err := removeKnownKey(a.t.Known, r.Host, r.Port, r.Fp)
		if err == nil && !ok {
			err = errors.New("that key is no longer in known_hosts")
		}
		return err
	}
	return a.t.removeHostKey(knownName(r.Host, r.Port))
}

type AliasReq struct {
	Alias string `json:"alias"`
}

func (a *App) findHost(alias string) (*Host, error) {
	cf, err := readConfig(a.t.Config)
	if err != nil {
		return nil, err
	}
	for _, h := range dedupeHosts(parseHosts(cf)) {
		if h.Alias == alias {
			return h, nil
		}
	}
	return nil, fmt.Errorf("%q is not in your SSH config", alias)
}

func (a *App) install(r AliasReq) error {
	h, err := a.findHost(r.Alias)
	if err != nil {
		return err
	}
	if h.IdentityFile == "" {
		return errors.New("this server has no key file set; manage it with this app first so it gets its own key")
	}
	remote, err := remoteInstallCmd(expandHome(h.IdentityFile)+".pub", h.Device, h.User)
	if err != nil {
		return err
	}
	logf, err := os.CreateTemp("", "sshkeys-*.log")
	if err != nil {
		return err
	}
	logf.Close()
	defer os.Remove(logf.Name())
	// Password only: offering keys the device doesn't know yet just uses up
	// its login attempts before the password prompt appears.
	code, err := runInTerminal(a.t.SSH, a.t.sshArgs("-o", "StrictHostKeyChecking=yes", "-o", "NumberOfPasswordPrompts=3",
		"-o", "PubkeyAuthentication=no", "-o", "PreferredAuthentications=keyboard-interactive,password",
		"-E", logf.Name(), h.Alias, remote)...)
	if err != nil {
		return err
	}
	if code != 0 {
		data, _ := os.ReadFile(logf.Name())
		detail := lastLines(string(data), 2)
		if detail == "" {
			detail = fmt.Sprintf("ssh exited with code %d", code)
		}
		return errors.New("key install failed: " + detail)
	}
	return nil
}

// pubkey returns the public key line to paste into a device by hand.
func (a *App) pubkey(r AliasReq) (string, error) {
	h, err := a.findHost(r.Alias)
	if err != nil {
		return "", err
	}
	if h.IdentityFile == "" {
		return "", errors.New("this server has no key file set")
	}
	data, err := os.ReadFile(expandHome(h.IdentityFile) + ".pub")
	if err != nil {
		return "", fmt.Errorf("public key not found: %s.pub", h.IdentityFile)
	}
	return strings.TrimSpace(string(data)), nil
}

type FolderReq struct {
	Path string `json:"path"`
}

// setFolder moves the app to another folder for its config, keys and
// known_hosts, remembers the choice, and points ssh's own config at it.
func (a *App) setFolder(r FolderReq) error {
	p := expandHome(strings.Trim(strings.TrimSpace(r.Path), `"`))
	if p == "" || !filepath.IsAbs(p) {
		return errors.New("choose a folder with its full path")
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return fmt.Errorf("can't use that folder: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.t.use(p)
	a.pending = map[string][]string{}
	if err := saveSettings(settings{SSHDir: a.t.Dir}); err != nil {
		return err
	}
	a.linkErr = syncLink(a.t.defaultConfig(), a.t.Config, a.t.linked())
	return a.linkErr
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// openConfig opens the config file in Notepad, creating it first if needed.
func (a *App) openConfig() error {
	if !exists(a.t.Config) {
		if err := os.WriteFile(a.t.Config, nil, 0o600); err != nil {
			return err
		}
	}
	return openFile(a.t.Config)
}

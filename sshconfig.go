package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// The managed block markers are shared with the earlier PowerShell version of
// this tool, so a config it wrote is picked up as-is.
const (
	beginMark = "# >>> SSH Server Manager: managed hosts (edit with the app) >>>"
	endMark   = "# <<< SSH Server Manager <<<"

	// The link block lives in ~/.ssh/config (the only file ssh reads by itself)
	// and points at the app's folder when that is somewhere else.
	linkBegin = "# >>> SSH Server Manager: app folder (edit with the app) >>>"
	linkEnd   = "# <<< SSH Server Manager: app folder <<<"

	// Per-host notes the app keeps inside its own block, as "# sshkeys: k=v".
	notePrefix = "# sshkeys:"
)

var errStale = errors.New("your SSH config changed on disk since it was loaded; it has been reloaded, please try again")

// Device types decide how the key gets installed and which key suits them.
const (
	devLinux   = "linux"
	devESXi    = "esxi"
	devWindows = "windows"
	devManual  = "manual" // switches, routers, firewalls: the key is pasted by hand
)

func normDevice(d string) string {
	switch d {
	case devESXi, devWindows, devManual:
		return d
	}
	return devLinux
}

// defaultKeyKind: ESXi (FIPS mode) and most network gear refuse ed25519 keys.
func defaultKeyKind(device string) string {
	if device == devESXi || device == devManual {
		return "rsa"
	}
	return "ed25519"
}

// legacyLines turn older algorithms back on for one host. Every name here is
// known to OpenSSH 7 through 10, so the config stays valid when it is shared
// between computers. PubkeyAcceptedKeyTypes is the spelling all versions accept.
var legacyLines = []string{
	"KexAlgorithms +diffie-hellman-group-exchange-sha1,diffie-hellman-group14-sha1,diffie-hellman-group1-sha1",
	"HostKeyAlgorithms +ssh-rsa",
	"PubkeyAcceptedKeyTypes +ssh-rsa",
	"Ciphers +aes128-cbc,aes192-cbc,aes256-cbc,3des-cbc",
	"MACs +hmac-sha1,hmac-sha1-96,hmac-md5",
}

var legacyKeywords = map[string]bool{
	"kexalgorithms": true, "hostkeyalgorithms": true, "pubkeyacceptedkeytypes": true,
	"pubkeyacceptedalgorithms": true, "ciphers": true, "macs": true,
}

// legacyArgs is the same set as -o options, for one-off ssh calls.
func legacyArgs() []string {
	var out []string
	for _, l := range legacyLines {
		out = append(out, "-o", strings.Replace(l, " ", "=", 1))
	}
	return out
}

// Host is one Host block from ~/.ssh/config.
type Host struct {
	Alias        string
	HostName     string
	User         string
	Port         string
	IdentityFile string
	Device       string   // managed entries only
	Legacy       bool     // managed entries only: older algorithms allowed
	Managed      bool     // inside our marked block
	Start, End   int      // line range in the file
	Extra        []string // keywords we don't manage (ProxyJump, ...)
}

type configFile struct {
	Lines []string
	B, E  int // marker line indexes, -1 when there is no block yet
	Stamp int64
}

var (
	kwRe        = regexp.MustCompile(`^([A-Za-z]+)(?:\s*=\s*|\s+)(.+)$`)
	hostMatchRe = regexp.MustCompile(`(?i)^\s*(host|match)(\s|=)`)
)

func fileStamp(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.ModTime().UnixNano() ^ st.Size()
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n"), nil
}

func writeLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return err
		}
	}
	nl := "\n"
	if runtime.GOOS == "windows" {
		nl = "\r\n"
	}
	return os.WriteFile(path, []byte(strings.Join(lines, nl)+nl), 0o600)
}

func readConfig(path string) (*configFile, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	cf := &configFile{Lines: lines, B: -1, E: -1, Stamp: fileStamp(path)}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == beginMark && cf.B < 0 {
			cf.B = i
		} else if t == endMark && cf.B >= 0 && cf.E < 0 {
			cf.E = i
		}
	}
	if cf.E < 0 {
		cf.B = -1
	}
	return cf, nil
}

// parseHosts returns the Host entries the app can show. Wildcard or multi-name
// Host lines and Match blocks are skipped and left untouched in the file; the
// one exception is the app's own "Host <name> <address>" form.
func parseHosts(cf *configFile) []*Host {
	var out []*Host
	var cur *Host
	flush := func() {
		if cur != nil {
			out = append(out, cur)
			cur = nil
		}
	}
	for n, line := range cf.Lines {
		if cf.B >= 0 && (n == cf.B || n == cf.E) {
			flush()
			continue
		}
		inBlock := cf.B >= 0 && n > cf.B && n < cf.E
		t := strings.TrimSpace(line)
		if cur != nil && cur.Managed && strings.HasPrefix(t, notePrefix) {
			for _, kv := range strings.Fields(strings.TrimPrefix(t, notePrefix)) {
				if v, ok := strings.CutPrefix(kv, "device="); ok {
					cur.Device = normDevice(v)
				}
			}
			continue
		}
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		m := kwRe.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		kw, v := m[1], strings.TrimSpace(m[2])
		k := strings.ToLower(kw)
		if k == "host" || k == "match" {
			flush()
			if k == "host" && !strings.ContainsAny(v, "*?!,\"") {
				names := strings.Fields(v)
				if len(names) == 1 || (inBlock && len(names) == 2) {
					cur = &Host{Alias: names[0], HostName: names[0], Port: "22", Managed: inBlock, Start: n, End: n}
					if inBlock {
						cur.Device = devLinux
					}
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		cur.End = n
		val := strings.Trim(v, "\"")
		switch {
		case k == "hostname":
			cur.HostName = val
		case k == "user":
			cur.User = val
		case k == "port":
			cur.Port = val
		case k == "identityfile":
			if cur.IdentityFile == "" {
				cur.IdentityFile = val
			} else {
				cur.Extra = append(cur.Extra, "IdentityFile (multiple)")
			}
		case k == "identitiesonly":
		case cur.Managed && k == "userknownhostsfile":
		case cur.Managed && legacyKeywords[k]:
			cur.Legacy = true
		default:
			cur.Extra = append(cur.Extra, kw)
		}
	}
	flush()
	return out
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func expandHome(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

func underHome(p string) bool {
	rel, err := filepath.Rel(homeDir(), expandHome(p))
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// configPath renders a key path the way it is written into the config:
// ~/... when it lives under the home folder, always with forward slashes.
func configPathFor(p string) string {
	p = expandHome(p)
	if underHome(p) {
		rel, _ := filepath.Rel(homeDir(), p)
		return "~/" + filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

// blockOpts is what the managed block needs to know beyond the hosts.
type blockOpts struct {
	Known string // known_hosts to name per host; empty when it is ssh's own
}

// hostBlock renders the app's section. Each server answers to its name and,
// unless an earlier server already claimed it, to its address too, so
// "ssh web" and "ssh 203.0.113.10" both use the key.
func hostBlock(managed []*Host, o blockOpts) []string {
	block := []string{beginMark}
	claimed := map[string]bool{}
	for _, h := range managed {
		claimed[strings.ToLower(h.Alias)] = true
	}
	for _, h := range managed {
		first := "Host " + h.Alias
		if a := strings.ToLower(h.HostName); a != "" && !claimed[a] {
			claimed[a] = true
			first += " " + h.HostName
		}
		block = append(block, first)
		if d := normDevice(h.Device); d != devLinux {
			block = append(block, "    "+notePrefix+" device="+d)
		}
		block = append(block, "    HostName "+h.HostName)
		if h.User != "" {
			block = append(block, "    User "+h.User)
		}
		block = append(block, "    Port "+h.Port)
		if h.IdentityFile != "" {
			block = append(block, `    IdentityFile "`+configPathFor(h.IdentityFile)+`"`, "    IdentitiesOnly yes")
		}
		if o.Known != "" {
			block = append(block, `    UserKnownHostsFile "`+configPathFor(o.Known)+`"`)
		}
		if h.Legacy {
			for _, l := range legacyLines {
				block = append(block, "    "+l)
			}
		}
		block = append(block, "")
	}
	return append(block, endMark)
}

// writeConfig rewrites only the managed block (creating it the first time,
// just before the first Host/Match line so global options stay global), drops
// the given line numbers (an unmanaged entry being taken over), and keeps
// everything else byte-for-byte. A .bak copy is written first.
func writeConfig(path string, managed []*Host, drop map[int]bool, expectStamp int64, o blockOpts) error {
	cf, err := readConfig(path)
	if err != nil {
		return err
	}
	if cf.Stamp != expectStamp {
		return errStale
	}

	block := hostBlock(managed, o)

	split := cf.B
	if split < 0 {
		split = len(cf.Lines)
		for n, l := range cf.Lines {
			if hostMatchRe.MatchString(l) {
				split = n
				break
			}
		}
	}

	var before, after []string
	for n, l := range cf.Lines {
		if drop[n] {
			continue
		}
		if cf.B >= 0 && n >= cf.B && n <= cf.E {
			continue
		}
		if n < split {
			before = append(before, l)
		} else {
			after = append(after, l)
		}
	}

	all := append([]string{}, before...)
	if cf.B < 0 && len(before) > 0 && strings.TrimSpace(before[len(before)-1]) != "" {
		all = append(all, "")
	}
	all = append(all, block...)
	if cf.B < 0 && len(after) > 0 && strings.TrimSpace(after[0]) != "" {
		all = append(all, "")
	}
	all = append(all, after...)
	return writeLines(path, all)
}

// includeLines points ssh at the app's config. The Windows ssh wants X:/...,
// the one that ships with Git wants /x/...; each ignores the other form.
func includeLines(cfg string) []string {
	p := configPathFor(cfg)
	lines := []string{`Include "` + p + `"`}
	if len(p) > 2 && p[1] == ':' {
		lines = append(lines, `Include "/`+strings.ToLower(p[:1])+p[2:]+`"`)
	}
	return lines
}

// syncLink keeps the link block at the top of ssh's own config in step with
// the folder the app is using: present when that folder is elsewhere, gone
// when it is ~/.ssh itself. It only writes when something has to change.
func syncLink(defaultCfg, activeCfg string, linked bool) error {
	lines, err := readLines(defaultCfg)
	if err != nil {
		return err
	}
	b, e := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == linkBegin && b < 0 {
			b = i
		} else if t == linkEnd && b >= 0 && e < 0 {
			e = i
		}
	}
	if e < 0 {
		b = -1
	}
	var want []string
	if linked {
		want = append(append([]string{linkBegin}, includeLines(activeCfg)...), linkEnd)
	}
	if b < 0 && want == nil {
		return nil
	}
	if b == 0 && want != nil && strings.Join(lines[b:e+1], "\n") == strings.Join(want, "\n") {
		return nil
	}
	var rest []string
	if b >= 0 {
		rest = append(rest, lines[:b]...)
		tail := lines[e+1:]
		if len(tail) > 0 && strings.TrimSpace(tail[0]) == "" {
			tail = tail[1:]
		}
		rest = append(rest, tail...)
	} else {
		rest = lines
	}
	out := append([]string{}, want...)
	if want != nil && len(rest) > 0 {
		out = append(out, "")
	}
	out = append(out, rest...)
	if len(out) == 0 && !exists(defaultCfg) {
		return nil
	}
	return writeLines(defaultCfg, out)
}

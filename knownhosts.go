package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"regexp"
	"strings"
)

type HostKey struct {
	Type string `json:"type"`
	Fp   string `json:"fp"`
}

type khEntry struct {
	Host string
	Port string
	Keys []HostKey
}

type knownHosts struct {
	Map    map[string]*khEntry // "host|port" (host lowercased)
	Order  []string
	Hashed int
	Stamp  int64
}

var bracketRe = regexp.MustCompile(`^\[(.+)\]:(\d+)$`)

func fingerprint(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
}

func knownName(host, port string) string {
	if port == "22" || port == "" {
		return host
	}
	return "[" + host + "]:" + port
}

func khKey(host, port string) string { return strings.ToLower(host) + "|" + port }

// readKnownHosts indexes plain (unhashed) known_hosts lines by host and port.
// Hashed lines can't be listed by name and are only counted; they are looked
// up per server with ssh-keygen -F. @cert-authority / @revoked lines and
// wildcard patterns are left alone.
func readKnownHosts(path string) *knownHosts {
	kh := &knownHosts{Map: map[string]*khEntry{}, Stamp: fileStamp(path)}
	data, err := os.ReadFile(path)
	if err != nil {
		return kh
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "@") {
			continue
		}
		p := strings.Fields(t)
		if len(p) < 3 {
			continue
		}
		if strings.HasPrefix(p[0], "|") {
			kh.Hashed++
			continue
		}
		fp := fingerprint(p[2])
		for _, n := range strings.Split(p[0], ",") {
			if n == "" || strings.ContainsAny(n, "*?!") {
				continue
			}
			host, port := n, "22"
			if m := bracketRe.FindStringSubmatch(n); m != nil {
				host, port = m[1], m[2]
			}
			k := khKey(host, port)
			e := kh.Map[k]
			if e == nil {
				e = &khEntry{Host: host, Port: port}
				kh.Map[k] = e
				kh.Order = append(kh.Order, k)
			}
			dup := false
			for _, hk := range e.Keys {
				if hk.Fp == fp {
					dup = true
				}
			}
			if !dup {
				e.Keys = append(e.Keys, HostKey{Type: p[1], Fp: fp})
			}
		}
	}
	return kh
}

// parseKeyLines extracts host keys from ssh-keyscan / ssh-keygen -F output.
func parseKeyLines(out string) (lines []string, keys []HostKey) {
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		p := strings.Fields(t)
		if len(p) < 3 || !keyTypeRe.MatchString(p[1]) || !b64Re.MatchString(p[2]) {
			continue
		}
		lines = append(lines, p[0]+" "+p[1]+" "+p[2])
		keys = append(keys, HostKey{Type: p[1], Fp: fingerprint(p[2])})
	}
	return
}

var (
	keyTypeRe = regexp.MustCompile(`^(ssh-|ecdsa-|sk-)[A-Za-z0-9@.\-]+$`)
	b64Re     = regexp.MustCompile(`^[A-Za-z0-9+/=]+$`)
)

// hashedNameMatches checks a hashed known_hosts name ("|1|salt|hash").
func hashedNameMatches(field, name string) bool {
	p := strings.Split(field, "|")
	if len(p) != 4 || p[1] != "1" {
		return false
	}
	salt, err1 := base64.StdEncoding.DecodeString(p[2])
	want, err2 := base64.StdEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil {
		return false
	}
	m := hmac.New(sha1.New, salt)
	m.Write([]byte(name))
	return hmac.Equal(m.Sum(nil), want)
}

// removeKnownKey removes one host key, picked by fingerprint, for one host.
// (ssh-keygen -R can only remove all of a host's keys.) A line that also
// names other hosts keeps them. The previous file is kept as known_hosts.old,
// the same as ssh-keygen does.
func removeKnownKey(path, host, port, fp string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	name := knownName(host, port)
	nl := "\n"
	if strings.Contains(string(data), "\r\n") {
		nl = "\r\n"
	}
	var out []string
	removed := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		p := strings.Fields(t)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "@") || len(p) < 3 || fingerprint(p[2]) != fp {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(p[0], "|") {
			if hashedNameMatches(p[0], name) {
				removed = true
			} else {
				out = append(out, line)
			}
			continue
		}
		var keep []string
		hit := false
		for _, n := range strings.Split(p[0], ",") {
			if strings.EqualFold(n, name) {
				hit = true
			} else if n != "" {
				keep = append(keep, n)
			}
		}
		if !hit {
			out = append(out, line)
			continue
		}
		removed = true
		if len(keep) > 0 {
			out = append(out, strings.Join(keep, ",")+" "+strings.Join(p[1:], " "))
		}
	}
	if !removed {
		return false, nil
	}
	if err := os.WriteFile(path+".old", data, 0o600); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(strings.Join(out, nl)), 0o600)
}

func appendKnownHosts(path string, lines []string) error {
	prefix := ""
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(prefix + strings.Join(lines, "\n") + "\n")
	return err
}

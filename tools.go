package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

type Tools struct {
	SSH, Keygen, Keyscan string
	Add                  string // ssh-add; empty when it isn't installed
	// Default is the folder ssh itself reads (~/.ssh). Dir is the folder the
	// app works in; when the two differ, Default's config links to Dir's.
	Default            string
	Dir, Config, Known string
	// Dev is set when SSHKEYS_SSH_DIR stands in for ~/.ssh (testing): ssh is
	// then told explicitly which config and known_hosts to use.
	Dev bool
}

func findExe(name string) (string, error) {
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("WINDIR"), "System32", "OpenSSH", name+".exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s was not found. Install \"OpenSSH Client\" from Windows Settings > System > Optional features", name)
}

func newTools() (*Tools, error) {
	t := &Tools{}
	var err error
	if t.SSH, err = findExe("ssh"); err != nil {
		return nil, err
	}
	if t.Keygen, err = findExe("ssh-keygen"); err != nil {
		return nil, err
	}
	if t.Keyscan, err = findExe("ssh-keyscan"); err != nil {
		return nil, err
	}
	t.Add, _ = findExe("ssh-add")
	t.Default = filepath.Join(homeDir(), ".ssh")
	if d := os.Getenv("SSHKEYS_SSH_DIR"); d != "" {
		t.Default, t.Dev = d, true
	}
	if d := os.Getenv("SSHKEYS_SSH_ADD"); d != "" && t.Dev {
		t.Add = d // a stand-in agent for tests
	}
	if err := os.MkdirAll(t.Default, 0o700); err != nil {
		return nil, err
	}
	t.use(t.chooseDir())
	return t, os.MkdirAll(t.Dir, 0o700)
}

func (t *Tools) use(dir string) {
	t.Dir = filepath.Clean(dir)
	t.Config = filepath.Join(t.Dir, "config")
	t.Known = filepath.Join(t.Dir, "known_hosts")
}

// linked reports whether the app's folder is somewhere other than ~/.ssh.
func (t *Tools) linked() bool { return !samePath(t.Dir, t.Default) }

func (t *Tools) defaultConfig() string { return filepath.Join(t.Default, "config") }

func (t *Tools) blockOpts() blockOpts {
	if t.linked() {
		return blockOpts{Known: t.Known}
	}
	return blockOpts{}
}

// secure tightens a file's permissions when the app works outside ~/.ssh,
// where a folder's inherited permissions may be wider than ssh tolerates for
// keys and config files.
func (t *Tools) secure(path string) {
	if t.linked() {
		secureFile(path)
	}
}

// sshArgs prefixes ssh options needed when running against a test folder.
func (t *Tools) sshArgs(args ...string) []string {
	if !t.Dev {
		return args
	}
	return append([]string{"-F", t.defaultConfig(), "-o", "UserKnownHostsFile=" + t.Known}, args...)
}

type runResult struct {
	Out  string
	Code int
}

func run(timeout time.Duration, exe string, args ...string) (runResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.WaitDelay = 2 * time.Second // never wait on a pipe a stray child kept open
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	res := runResult{Out: strings.TrimSpace(string(out))}
	var ee *exec.ExitError
	switch {
	case err == nil, errors.Is(err, exec.ErrWaitDelay):
	case ctx.Err() != nil:
		return res, fmt.Errorf("%s timed out", filepath.Base(exe))
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
	default:
		return res, err
	}
	return res, nil
}

func (t *Tools) removeHostKey(name string) error {
	if _, err := os.Stat(t.Known); err != nil {
		return nil
	}
	_, err := run(20*time.Second, t.Keygen, "-R", name, "-f", t.Known)
	return err
}

// lookupHostKeys asks ssh-keygen, which understands hashed entries.
func (t *Tools) lookupHostKeys(name string) []HostKey {
	if _, err := os.Stat(t.Known); err != nil {
		return nil
	}
	r, err := run(10*time.Second, t.Keygen, "-F", name, "-f", t.Known)
	if err != nil {
		return nil
	}
	_, keys := parseKeyLines(r.Out)
	return keys
}

// reachable is a quick TCP check, so a wrong address or a device that is off
// is reported in a few seconds instead of after ssh's own timeouts.
func reachable(host, port string) error {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		var ne net.Error
		switch {
		case errors.As(err, &ne) && ne.Timeout():
			return fmt.Errorf("no answer from %s on port %s. Check the address, that the device is on, and that this computer can reach it (VPN)", host, port)
		case strings.Contains(err.Error(), "refused"):
			return fmt.Errorf("%s refused the connection on port %s. SSH may be turned off on the device, or it uses a different port", host, port)
		case strings.Contains(err.Error(), "no such host"):
			return fmt.Errorf("the name %s could not be found. Check the spelling or use the IP address", host)
		}
		return fmt.Errorf("couldn't connect to %s on port %s: %v", host, port, err)
	}
	c.Close()
	return nil
}

// keyscan fetches the host keys a server offers. Older devices that
// ssh-keyscan can't negotiate with are asked again through ssh itself with
// the older algorithms switched on.
func (t *Tools) keyscan(host, port string) ([]string, []HostKey, string) {
	if err := reachable(host, port); err != nil {
		return nil, nil, err.Error()
	}
	args := []string{"-T", "6"}
	if port != "22" {
		args = append(args, "-p", port)
	}
	args = append(args, host)
	r, err := run(25*time.Second, t.Keyscan, args...)
	if err == nil {
		if lines, keys := parseKeyLines(r.Out); len(keys) > 0 {
			return lines, keys, ""
		}
	}
	lines, keys, msg := t.probeHostKey(host, port)
	if len(keys) == 0 && msg == "" {
		msg = "the device answered but didn't offer an SSH host key"
	}
	return lines, keys, msg
}

// probeHostKey lets ssh connect far enough to record the host key in a
// throwaway known_hosts file, without logging in.
func (t *Tools) probeHostKey(host, port string) ([]string, []HostKey, string) {
	tmp, err := os.MkdirTemp("", "sshkeys-probe-")
	if err != nil {
		return nil, nil, err.Error()
	}
	defer os.RemoveAll(tmp)
	kh := filepath.Join(tmp, "known_hosts")
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=" + kh,
		"-o", "GlobalKnownHostsFile=" + filepath.Join(tmp, "none"), "-o", "HashKnownHosts=no", "-o", "CheckHostIP=no",
		"-o", "ConnectTimeout=8", "-o", "PreferredAuthentications=none", "-o", "IdentitiesOnly=yes"}
	args = append(args, legacyArgs()...)
	args = append(args, "-p", port, host, "exit")
	if t.Dev {
		args = append([]string{"-F", t.defaultConfig()}, args...)
	}
	r, _ := run(25*time.Second, t.SSH, args...)
	data, _ := os.ReadFile(kh)
	_, keys := parseKeyLines(string(data))
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if p := strings.Fields(l); len(p) >= 3 && keyTypeRe.MatchString(p[1]) && b64Re.MatchString(p[2]) {
			lines = append(lines, knownName(host, port)+" "+p[1]+" "+p[2])
		}
	}
	if len(keys) == 0 {
		return nil, nil, lastLines(r.Out, 2)
	}
	return lines, keys, ""
}

// ensureKey creates the key pair when it isn't there yet. kind is "ed25519"
// or "rsa" (for ESXi and older devices).
func (t *Tools) ensureKey(path, alias, kind string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		if _, err := os.Stat(path + ".pub"); err != nil {
			return false, fmt.Errorf("found %s but not %s.pub; recreate it with: ssh-keygen -y -f \"%s\" > \"%s.pub\"", path, path, path, path)
		}
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	// The label only names the server. It does not include this PC's name,
	// which would otherwise be copied to every server the key is installed on.
	comment := sanitize("sshkeys-" + alias)
	typeArgs := []string{"-t", "ed25519"}
	if kind == "rsa" {
		typeArgs = []string{"-t", "rsa", "-b", "3072"}
	}
	// No passphrase on purpose: AI apps connect non-interactively.
	args := append([]string{"-q"}, typeArgs...)
	args = append(args, "-N", "", "-C", comment, "-f", path)
	r, err := run(60*time.Second, t.Keygen, args...)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path + ".pub"); err != nil {
		return false, fmt.Errorf("ssh-keygen could not create %s: %s", path, r.Out)
	}
	t.secure(path)
	return true, nil
}

// keyKind reads the key type from a public key file ("ed25519", "rsa", ...).
func keyKind(pubPath string) string {
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return ""
	}
	p := strings.Fields(string(data))
	if len(p) == 0 {
		return ""
	}
	switch {
	case p[0] == "ssh-ed25519":
		return "ed25519"
	case p[0] == "ssh-rsa":
		return "rsa"
	case strings.HasPrefix(p[0], "ecdsa-"):
		return "ecdsa"
	case strings.HasPrefix(p[0], "sk-"):
		return "fido"
	}
	return strings.TrimPrefix(p[0], "ssh-")
}

type TestResult struct {
	Kind    string `json:"kind"` // ok | auth | hostkey | algo | error
	Message string `json:"message"`
}

func (t *Tools) test(alias string) TestResult { return t.testAs(alias, "") }

// testArgs is what ssh is started with to try a passwordless login. A user
// name is given for entries the app doesn't manage, whose name typed in the
// window can't be saved.
func testArgs(alias, user string) []string {
	// LogLevel=VERBOSE makes ssh say "Authenticated to ...", which is the only
	// dependable sign on devices that have no "echo" command.
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "StrictHostKeyChecking=yes", "-o", "LogLevel=VERBOSE"}
	if user != "" {
		args = append(args, "-l", user)
	}
	return append(args, alias, "echo SSHKEYS_OK")
}

func (t *Tools) testAs(alias, user string) TestResult {
	if user != "" && !userRe.MatchString(user) {
		return TestResult{"error", "Enter a valid username"}
	}
	r, err := run(25*time.Second, t.SSH, t.sshArgs(testArgs(alias, user)...)...)
	switch {
	case strings.Contains(r.Out, "SSHKEYS_OK") || strings.Contains(r.Out, "Authenticated to "):
		return TestResult{"ok", "Logged in with the key, no password or prompts."}
	case err != nil:
		return TestResult{"error", err.Error()}
	case strings.Contains(r.Out, "no matching ") || strings.Contains(r.Out, "no mutual signature"):
		return TestResult{"algo", "This device only speaks older encryption that ssh turns off by default."}
	case strings.Contains(r.Out, "Host key verification failed") || strings.Contains(r.Out, "IDENTIFICATION HAS CHANGED") ||
		strings.Contains(r.Out, "No ED25519 host key is known") || strings.Contains(r.Out, "host key is known for"):
		return TestResult{"hostkey", "The server's host key isn't trusted or has changed."}
	case strings.Contains(r.Out, "Permission denied") || strings.Contains(r.Out, "Too many authentication failures"):
		return TestResult{"auth", "The server didn't accept the key yet."}
	default:
		msg := lastLines(r.Out, 2)
		if msg == "" {
			msg = fmt.Sprintf("ssh exited with code %d", r.Code)
		}
		return TestResult{"error", msg}
	}
}

// readPub returns "type base64" from a public key file.
func readPub(pubPath string) (string, error) {
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("public key not found: %s", pubPath)
	}
	p := strings.Fields(string(data))
	if len(p) < 2 || !keyTypeRe.MatchString(p[0]) || !b64Re.MatchString(p[1]) {
		return "", fmt.Errorf("unexpected public key format in %s", pubPath)
	}
	return p[0] + " " + p[1], nil
}

// remoteInstallCmd builds the one command that adds the public key on the
// device, idempotently, for the given device type.
func remoteInstallCmd(pubPath, device, user string) (string, error) {
	pub, err := readPub(pubPath)
	if err != nil {
		return "", err
	}
	// add appends the key unless it is already there, starting a new line
	// first when the file doesn't end with one.
	add := func(f string) string {
		return "(grep -qF '" + pub + "' " + f + " 2>/dev/null || { [ ! -s " + f + " ] || [ $(tail -c1 " + f + " | wc -l) -gt 0 ] || echo >> " + f + "; echo '" + pub + "' >> " + f + "; })"
	}
	switch normDevice(device) {
	case devESXi:
		// ESXi ignores ~/.ssh and reads /etc/ssh/keys-<user>/authorized_keys.
		// The file keeps its own permissions (they mark it for ESXi's config
		// backup), and auto-backup.sh makes the change survive a reboot.
		if !userRe.MatchString(user) {
			return "", errors.New("set the username for this server first (usually root on ESXi)")
		}
		d := "/etc/ssh/keys-" + user
		return "mkdir -p " + d + " && " + add(d+"/authorized_keys") + " && { /sbin/auto-backup.sh >/dev/null 2>&1 || true; }", nil
	case devWindows:
		return "powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand " + psEncode(windowsInstallScript(pub)), nil
	case devManual:
		return "", errors.New("this device type needs the key pasted in by hand")
	}
	return "umask 077; mkdir -p ~/.ssh && touch ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys && " +
		add("~/.ssh/authorized_keys"), nil
}

// windowsInstallScript adds the key for OpenSSH Server on Windows: always to
// the user's own authorized_keys, and for administrators also to the shared
// administrators_authorized_keys file that sshd uses for them by default.
func windowsInstallScript(pub string) string {
	return `$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
$k='` + pub + `'
function Add-Key($f){
  $t=''
  if(Test-Path $f){$t=[IO.File]::ReadAllText($f)}
  if($t.Contains($k)){return}
  if($t.Length -gt 0 -and -not $t.EndsWith("` + "`n" + `")){$t+="` + "`r`n" + `"}
  [IO.File]::WriteAllText($f,$t+$k+"` + "`r`n" + `",(New-Object Text.ASCIIEncoding))
}
$d=Join-Path $env:USERPROFILE '.ssh'
New-Item -ItemType Directory -Force -Path $d | Out-Null
Add-Key (Join-Path $d 'authorized_keys')
$p=New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if($p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){
  $f=Join-Path $env:ProgramData 'ssh\administrators_authorized_keys'
  Add-Key $f
  & icacls.exe $f /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F' | Out-Null
}
exit 0
`
}

// psEncode is PowerShell's -EncodedCommand format: base64 of UTF-16LE.
func psEncode(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, strings.TrimSpace(l))
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " ")
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && (r == '.' || r == '_' || r == '@' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

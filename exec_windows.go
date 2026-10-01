//go:build windows

package main

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow   = 0x08000000
	createNewConsole = 0x00000010
)

// mainHwnd is the app window; dialogs use it as their owner so they open in
// front of the app.
var mainHwnd uintptr

// hideWindow stops console tools (ssh, ssh-keygen) from flashing a window,
// since this is a GUI app with no console of its own.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// runInTerminal runs a command in its own visible console window and waits
// for it, so ssh can ask the user for a password directly.
func runInTerminal(exe string, args ...string) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func openFile(path string) error   { return exec.Command("notepad.exe", path).Start() }
func openFolder(path string) error { return exec.Command("explorer.exe", path).Start() }

// openURL hands a web address to the user's default browser.
func openURL(u string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", u).Start()
}

// secureFile gives a file the access list OpenSSH insists on for keys and
// config files: this user, SYSTEM and Administrators only, nothing inherited.
// Best effort: if it fails, ssh's own message says what it dislikes.
func secureFile(path string) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return
	}
	run(15*time.Second, "icacls.exe", path, "/inheritance:r",
		"/grant:r", "*"+u.User.Sid.String()+":F", "/grant:r", "*S-1-5-18:F", "/grant:r", "*S-1-5-32-544:F")
}

var (
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	ole32                   = windows.NewLazySystemDLL("ole32.dll")
	procSHBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	procCoUninitialize      = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree       = ole32.NewProc("CoTaskMemFree")
)

type browseInfo struct {
	Owner       uintptr
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}

// pickFolder shows the Windows folder chooser and returns the chosen path, or
// "" when it was cancelled. It runs on a thread of its own, so the app window
// keeps painting while the dialog is open.
func pickFolder(title string) (string, error) {
	type result struct {
		path string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		procCoInitializeEx.Call(0, 0x2) // COINIT_APARTMENTTHREADED
		defer procCoUninitialize.Call()

		t, err := windows.UTF16PtrFromString(title)
		if err != nil {
			done <- result{"", err}
			return
		}
		name := make([]uint16, windows.MAX_PATH)
		bi := browseInfo{
			Owner:       mainHwnd,
			DisplayName: &name[0],
			Title:       t,
			Flags:       0x0001 | 0x0010 | 0x0040, // file system folders only, edit box, new style (allows "New folder")
		}
		pidl, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			done <- result{"", nil}
			return
		}
		defer procCoTaskMemFree.Call(pidl)
		buf := make([]uint16, 32768)
		if ok, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0]))); ok == 0 {
			done <- result{"", errors.New("that location isn't a folder on disk")}
			return
		}
		done <- result{windows.UTF16ToString(buf), nil}
	}()
	r := <-done
	return r.path, r.err
}

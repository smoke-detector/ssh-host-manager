package main

import (
	"os"
	"os/user"
	"strings"
)

const appName = "SSH Host Manager"

// sourceURL is where anyone can read this code and build the app themselves.
const sourceURL = "https://github.com/smoke-detector/ssh-host-manager"

// noticeVersion changes when the first-run notice changes, so that it is
// shown again.
const noticeVersion = "1"

// The first-run notice. The README and manual carry the same points.
var noticeText = []string{
	"This app sets up SSH keys that have no passphrase, so that AI apps can log in without asking. " +
		"Anyone, and any AI app, using this Windows account can then run any command on the servers you add.",
	"Only add servers and devices that you own or are allowed to manage.",
	"The app is provided as is, with no warranty of any kind. You are responsible for what you allow it, and your AI apps, to do. " +
		"The author is not liable for any damage or loss.",
}

const noticeSource = "The app only talks to the servers you add. Its source code is public at github.com/smoke-detector/ssh-host-manager, " +
	"so you can check how it works or build it yourself."

// links are the only web addresses the app will ever open, and only in the
// user's own browser after a click. The app itself never contacts them.
var links = map[string]string{
	"donate": "https://donate.stripe.com/eVq6oz0LU7ucgX19KE57W00",
	"source": sourceURL,
}

// localUser is the name ssh logs in with when a server has no username set.
func localUser() string {
	name := os.Getenv("USERNAME")
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

package main

import (
	"os"
	"os/user"
	"strings"
)

const appName = "SSH Host Manager"

// links are the only web addresses the app will ever open, and only in the
// user's own browser after a click. The app itself never contacts them.
var links = map[string]string{
	"donate": "https://donate.stripe.com/eVq6oz0LU7ucgX19KE57W00",
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

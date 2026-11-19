package node

import "os/user"

var osUser = currentUser()

func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

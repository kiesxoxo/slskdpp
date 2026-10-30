//go:build !unix

package hooks

import "os/exec"

func ownGroup(*exec.Cmd) {}

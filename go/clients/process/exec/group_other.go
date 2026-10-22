//go:build !unix

package exec

import osexec "os/exec"

// ownGroup is a no-op where process groups are not available.
func ownGroup(*osexec.Cmd) {}

// ownSession is a no-op where sessions are not available.
func ownSession(*osexec.Cmd) {}

// terminate kills cmd's process: without process groups there is no gentler signal
// that reaches its children.
func terminate(cmd *osexec.Cmd) {
	_ = cmd.Process.Kill()
}

// kill kills cmd's process; one already gone is ignored.
func kill(cmd *osexec.Cmd) {
	_ = cmd.Process.Kill()
}

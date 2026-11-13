// Package github holds GitHub REST API replies for the github connector's tests, in the
// shapes GitHub's REST reference documents (API version 2022-11-28) for repository
// acme/widgets: the repository, its main branch at commit HeadSHA, and that commit's
// recursive tree, whole and truncated.
package github

import "embed"

// HeadSHA is the commit the main branch points at in branch.json.
const HeadSHA = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"

// MovedSHA is the commit branch_moved.json reports, after a push.
const MovedSHA = "beef0000beef0000beef0000beef0000beef0000"

// CommitDate is HeadSHA's committer date, as branch.json reports it.
const CommitDate = "2026-11-12T09:30:00Z"

// Replies are the reply bodies, by file name:
//   - repo.json: GET /repos/acme/widgets (default branch main).
//   - branch.json: GET /repos/acme/widgets/branches/main at HeadSHA.
//   - branch_moved.json: the same branch after a push, at MovedSHA.
//   - tree.json: GET /repos/acme/widgets/git/trees/<HeadSHA>?recursive=1 — six regular
//     files (one 50 MB), a symlink, a submodule and directory entries.
//   - tree_truncated.json: the same tree with truncated set.
//
//go:embed *.json
var Replies embed.FS

// Reply returns the named reply body; it panics on a missing name, a test bug.
func Reply(name string) string {
	body, err := Replies.ReadFile(name)
	if err != nil {
		panic(err)
	}

	return string(body)
}

// Package okf reads, renders and checks Open Knowledge Format (OKF) v0.2 bundles: a
// directory tree of markdown concepts, each with YAML frontmatter, plus the reserved
// index.md (directory listing) and log.md (dated history) files.
//
// Parse splits a file into its frontmatter, body and links; RenderFrontmatter writes
// frontmatter back in a stable key order; RenderIndex and RenderRootIndex write
// index.md; Conform and ConformLog report where a file breaks the format or a
// caller's per-type rules. Every function is pure: callers own the file I/O.
package okf

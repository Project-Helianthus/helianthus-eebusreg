// Command apiboundary enforces the repository layout and public API boundary of this module. Run
// from the repository root, it walks every tracked, untracked, ignored and on-disk path and
// rejects symlinks, case-fold collisions, any docs path segment and documentation files other
// than the root README.md and AGENTS.md. It then rejects public packages other than
// eebusruntime, eebusraw and eebusevidence, forbidden or missing frozen exports, and malformed
// build constraints; prose comments are not policed. Violations go to standard error with exit
// status 1, bad flags exit 2, and -manifest writes the canonical export manifest atomically to a
// path outside the repository. Its git queries are read-only.
package main

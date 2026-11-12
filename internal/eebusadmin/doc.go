// Package eebusadmin serves the local operator administration channel of the eeBUS runtime over
// an owner-only Unix domain socket inside the runtime directory. Only peers with the same
// effective user ID, checked through native peer credentials before any body byte is read, may
// connect; requests are version-tagged, length-prefixed frames of at most 64 KiB, connections and
// commands per session are bounded, and every read, write and handler call has a deadline. Start
// refuses a symlinked, foreign or wrong-type socket path and replaces only a socket that refuses
// connections; Close unlinks the socket only while it is still the one this process created. On
// platforms other than Linux and macOS, Start fails with admin_transport_unsupported.
package eebusadmin

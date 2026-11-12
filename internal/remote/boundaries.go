package remote

// RemoteWrites lists what `dot remote` creates on a host, in the shape the
// documentation names it: three trees anchored at the remote $HOME, the record
// directory under the mirror root, plus every image, volume and container
// carrying the host's prefix. Nothing else on a host is written, and nothing at
// all is written on this machine except the admission leases under the state
// root.
func RemoteWrites(h Host) []string {
	return []string{
		"$HOME/" + h.Root,
		"$HOME/" + h.Root + "/.dot-remote",
		"$HOME/" + h.GitRoot,
		"$HOME/" + h.SrcRoot,
		h.Image(),
		h.Prefix + "-<volume>",
		h.Prefix + "-<mirror key>",
	}
}

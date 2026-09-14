// Command hflock mirrors 国产 model weights from Hugging Face to Gitee AI /
// ModelScope and emits a SHA256 hash-provenance manifest: verify re-hashes
// the pin set (with an optional --check CI gate), sync mirrors it to the CN
// platforms, init generates a lockfile, list shows per-weight status.
package main

import "github.com/SuperMarioYL/hflock/internal/cli"

func main() {
	cli.Execute()
}

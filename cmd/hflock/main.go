// Command hflock mirrors 国产 model weights from Hugging Face to Gitee AI /
// ModelScope and emits a SHA256 hash-provenance manifest. v0.1 (m1) ships the
// lockfile parser + hash verifier.
package main

import "github.com/SuperMarioYL/hflock/internal/cli"

func main() {
	cli.Execute()
}

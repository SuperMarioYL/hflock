# hflock demo tape (vhs). Renders the m1 happy path to assets/demo.gif:
# build -> local fixture server (stands in for Hugging Face) -> verify -> manifest
# -> shasum cross-check. Fully offline; re-render with `vhs docs/demo.tape`.
Output assets/demo.gif

Set FontSize 14
Set Width 110
Set Height 32
Set Padding 6
Set Theme "Dracula"
Set TypingSpeed 12ms

Type "echo 'hflock: mirror and hash-verify CN model weights (m1: lockfile + SHA256)'"
Enter
Sleep 1s

Type "go build -o hflock ./cmd/hflock"
Enter
Sleep 3s

Type "python3 -m http.server 8053 --directory examples/fixture &"
Enter
Sleep 1500ms

Type "HFLOCK_HF_BASE=http://localhost:8053 ./hflock verify examples/weights.lock.yaml"
Enter
Sleep 3s

Type "cat weights.lock.manifest.json"
Enter
Sleep 4s

Type "shasum -a 256 examples/fixture/deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json"
Enter
Sleep 3s

Type "echo '# the sha256 above matches the manifest entry for config.json - provenance verified.'"
Enter
Sleep 2s

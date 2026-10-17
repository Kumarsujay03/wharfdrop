# Contributing to Wharfdrop

Thanks for taking the time to contribute!

## Reporting bugs and requesting features

- Search existing issues first to avoid duplicates.
- Use the **Bug report** or **Feature request** template and include your OS on both devices,
  how they connect to the network (Wi-Fi band or Ethernet), and the output of `wharfdrop status`.
- For security problems, follow the [security policy](SECURITY.md) instead of opening a public issue.

## Development setup

You need Go 1.22 or newer. There are no third-party dependencies.

```bash
git clone https://github.com/Kumarsujay03/wharfdrop.git
cd wharfdrop
make test      # unit tests and a two-node end-to-end test, with the race detector
make build     # bin/wharfdrop for your platform
./build.sh     # release binaries for every platform into dist/
```

On Windows, where `make` is not installed, use `powershell -ExecutionPolicy Bypass -File build.ps1`,
or run `go test ./...` and `go build -o bin\wharfdrop.exe ./cmd/wharfdrop` directly.

To run two nodes on one machine, give each its own config directory and ports:

```bash
mkdir -p /tmp/node-b
echo '{"name":"node-b","peer_port":48090,"ui_port":48091}' > /tmp/node-b/config.json
WHARFDROP_DIR=/tmp/node-b ./bin/wharfdrop serve
WHARFDROP_DIR=/tmp/node-b ./bin/wharfdrop pair 127.0.0.1:48080
```

## Making changes

1. Fork the repository and create a branch: `git checkout -b fix/short-description`.
2. Keep the change focused on one thing and follow the style of the surrounding code.
3. Run `make fmt vet test` before you push. CI runs the same checks on Linux, Windows and macOS.
4. Commit with a clear message that says what changed and why.
5. Open a pull request, fill in the template, and link any related issue (for example `Fixes #12`).

Changes to pairing, TLS or path handling get extra review. Please explain the security
reasoning in the pull request.

## Pull request guidelines

- Small pull requests are easier to review and get merged faster.
- Update the README or CHANGELOG if your change affects how the project is used.
- Be patient and respectful during review. Feedback is about the code, not about you.

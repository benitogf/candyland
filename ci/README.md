# Release

The release pipeline now lives at `.github/workflows/release.yml` (active). A version
tag (`v*`) triggers it: it builds the embedded SPA + the cross-platform binaries
(linux/darwin/windows × amd64/arm64; webview/CGO on linux-amd64 + windows via Bazel +
a Zig toolchain, headless elsewhere) and publishes the GitHub Release the detritus
installer pulls from.

Cut a release from `main` with:

```bash
scripts/release.sh X.Y.Z
```

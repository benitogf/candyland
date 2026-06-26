# Release workflow

`ci/release.yml` is the release pipeline (linux/darwin/windows × amd64/arm64;
builds the embedded SPA + binaries via Bazel + a Zig toolchain — webview/CGO on
linux-amd64 + windows, headless elsewhere — and publishes the GitHub Release the
detritus installer pulls from). It lives here, not under `.github/workflows/`,
because the CLI token used to push these branches lacks the `workflow` OAuth scope.

To activate it (one-time, needs a workflow-scoped token — a human step):

```bash
gh auth refresh -h github.com -s workflow
git mv ci/release.yml .github/workflows/release.yml
git commit -m 'ci: activate release workflow' && git push
```

Then cut a release with `scripts/release.sh X.Y.Z`. Until activated there are no
candyland releases, and the detritus installer simply skips the sidecar binary.

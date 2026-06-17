# Release workflow

`ci/release.yml` is the standalone-binary release pipeline. It lives here (not under
.github/workflows/) because the CLI token used to push lacks the `workflow` OAuth
scope. To activate it, move it into place and push with a workflow-scoped token:

```bash
gh auth refresh -h github.com -s workflow   # one-time
git mv ci/release.yml .github/workflows/release.yml
git commit -m 'ci: activate release workflow' && git push
```

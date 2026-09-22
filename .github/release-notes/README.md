# Release notes

One file per version (`vX.Y.Z.md`), bilingual (`## English` → `## 中文` →
`---` → `Full Changelog` link).

`release.yml` reads `.github/release-notes/<tag>.md` at publish time and uses it
as the Release body (the annotated-tag body is unreliable on the runner, which
stores a tag push as a lightweight ref). So: write the notes here, commit them,
then push the `v*` tag and the body is correct automatically.

You can still override by hand with `gh` afterwards:

```sh
gh release edit v1.0.10 --notes-file v1.0.10.md   # or: --notes "..."
```

(Editing a file here does not retroactively change an already-published Release;
run the `gh release edit` command above to push a change.)


# Release notes archive

These files are **archives / drafts only** — nothing here is published
automatically.

Release bodies are maintained by hand with `gh`:

```sh
gh release edit v1.0.9 --notes-file notes.md   # or: --notes "..."
```

Editing a file in this folder does **not** change any published Release.
Whenever you write notes here for a new version, either pass the same file to
`git tag -a vX.Y.Z -F <file>` before pushing the tag, or apply it afterwards
with the `gh release edit` command above.

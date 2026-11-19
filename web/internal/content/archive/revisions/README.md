# Dated documentation revisions (#1073)

Each directory here is one revision of the public technical documentation
and the security page. A revision is never edited: its manifest's SHA-256
is pinned in `../pinned.go`. Verification checks every declared file and
rejects extra files or duplicate declarations.

To capture a new revision, start from a clean checkout of the deployed
commit (no tracked, untracked or ignored changes). From `apps/web`:

```sh
go build -buildvcs=true -o /tmp/kiff-archive-docs ./cmd/archive-docs
/tmp/kiff-archive-docs -commit "$(git rev-parse HEAD)"
```

The capture requires Git VCS metadata in the binary, a matching checkout
HEAD, and an unchanged working tree. Images are checked against the
committed bytes even when `-static` points to another directory. Build
outside the checkout so the binary itself does not dirty it. Unstamped
binaries (including a `go run` build without VCS metadata) are refused.

Capture writes a new directory and prints the manifest digest to add to
`../pinned.go`. Never change an existing revision or its pin. The DPA and
the Terms link the revision they mean.

# Contributing to Share2Us CLI

Thanks for your interest in improving the Share2Us CLI. Bug reports, feature
ideas, and pull requests are all welcome.

## Reporting bugs and requesting features

Open an issue with:

- what you ran (the exact command) and what you expected,
- what actually happened (include `s2u version` and your OS),
- the smallest steps that reproduce it.

Please **don't** paste real share links, tokens, or file contents into issues.

## Security issues

Do **not** open a public issue for a vulnerability. Email
**support@share2.us** with the details and we'll coordinate a fix and
disclosure.

## Development

Requires **Go 1.25+**. Most of the logic lives in the companion library
[share2us/cli-core](https://github.com/share2us/cli-core); this repo is the
command-line front end.

```sh
git clone https://github.com/share2us/cli
cd cli
go build -o s2u .          # build
go test ./...              # tests
go vet ./...               # vet
gofmt -l .                 # formatting (should print nothing)
```

Point a dev build at a non-production environment with
`s2u config set-base-url <domain>` or `SHARE2US_BASE_URL`.

### Working against a local `cli-core`

If your change spans both repos, add a temporary `replace` to `go.mod`:

```
replace github.com/share2us/cli-core => ../cli-core
```

Remove it before committing — `main` must build against the tagged `cli-core`
release.

## Pull requests

- Keep changes focused; one logical change per PR.
- Add or update tests for behavior you change.
- Run `gofmt`, `go vet`, and `go test ./...` before pushing — CI runs them too.
- Match the commit style already in the log: `scope: short summary`
  (e.g. `cli: friendlier error when the cloud is unreachable`).
- Describe user-visible changes in the PR body; if you touch command syntax,
  update the usage text in `cli-core` and the README.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE.md).

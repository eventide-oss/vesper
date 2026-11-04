# Vesper

Ephemeral preview environments for Kubernetes, one per pull request.

Vesper is a Kubernetes operator and a CLI (`vesperctl`). You declare a
`PreviewEnvironment`; Vesper creates an isolated environment for it, and
removes it automatically when its time to live (TTL) expires.

> **Status:** pre-alpha. Nothing here is usable yet.

## Build

```sh
make build
./bin/vesperctl version
```

## Contributing

Read [AGENTS.md](AGENTS.md) and the skills in `.claude/skills/` before you
start. In short: every change starts from an issue, uses a
`<type>/<issue-number>-<description>` branch and Conventional Commits, and
ends in a small pull request that follows
[the template](.github/PULL_REQUEST_TEMPLATE.md).

## License

Apache-2.0.

# Contributing

English | [中文](CONTRIBUTING.zh.md)

This repository holds the engine and the `oberth` CLI. The macOS client and the
coding-agent skill are separate products in their own repositories; changes to
them do not belong here.

## Before you change anything

1. Confirm the change belongs to this product. [docs/product.md](docs/product.md)
   says what the project is for and what it deliberately does not do.
2. Read the entry points first: [docs/agent/develop.md](docs/agent/develop.md) for
   build and check commands, [docs/agent/architecture.md](docs/agent/architecture.md)
   for the chain of facts a change has to attach to.
3. A change to the CLI, RPC, JSON or manifest is a contract change: update the
   generated schema, the reference in [docs/cli.md](docs/cli.md) and the
   regression in the same pull request.

## Checks

Run what you changed, and report the inputs and results you actually had.
[docs/agent/validation.md](docs/agent/validation.md) says what each check proves
and where it is safe to run.

Anything that starts a service needs an isolated `HOME` and `BERTH_HOME` and
throwaway projects; reclaim the processes you started. Never point a test at an
everyday runtime service, ledger or a real repository.

Do not relax a check, the required-check list or a workflow gate to make a change
pass; fix the change instead.

## Pull requests

- One topic per branch and pull request, with several small commits per topic.
- Say what you ran, what it produced, and what you did not run. The pull request
  template asks for exactly that; a formatting check is not a test.
- The maintainer merges through the pull request once the required checks are
  green. Do not merge early, and do not push directly to `main`.

## Contributions and licensing

Contributions are accepted under the [MIT license](LICENSE). Third-party sources,
fonts and site assets keep their own licenses: read
[third_party/README.md](third_party/README.md) before adding any.

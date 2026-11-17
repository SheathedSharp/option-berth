# Security policy

English | [中文](SECURITY.zh.md)

## Supported versions

Fixes land on `main` and ship with the next tagged release. Only the newest
release is supported; older tags stay as history, not as maintained branches.

## Reporting a vulnerability

Do not open a public issue, pull request or discussion for a vulnerability.
Report it privately through GitHub:

<https://github.com/SheathedSharp/option-berth/security/advisories/new>

Include:

- the exact version (`oberth version --json`) and the platform you ran it on;
- the commands, manifest or configuration needed to reproduce it;
- what you observed, and what you expected instead.

Use synthetic paths and throwaway repositories. Do not attach credentials,
private repository contents, everyday runtime logs, or screenshots of a
machine you do not own.

## Scope

In scope: the engine, the `oberth` CLI, the JSON and RPC surface, manifest
parsing, and the install scripts in this repository.

Out of scope here: the macOS client and the coding-agent skill are separate
products with their own repositories. Third-party tools, agent CLIs and model
services the engine merely invokes are out of scope; report those upstream.

## What happens next

This is a single-maintainer project, so no response time is promised. A
confirmed report is fixed on `main`, released through the normal release
process, and credited in the release notes unless you ask otherwise.

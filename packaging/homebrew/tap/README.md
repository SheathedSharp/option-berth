# SheathedSharp Homebrew tap

Homebrew formulae for SheathedSharp projects.

## option-berth

```sh
brew install SheathedSharp/tap/option-berth
```

The formula installs `oberth` and `jev-attention` from the engine release
archives, together with the licence and build manifest. It supports macOS and
Linux on amd64 and arm64.

Upgrade and uninstall:

```sh
brew upgrade option-berth
brew uninstall option-berth
```

`brew uninstall` removes the binaries and the files inside the Homebrew prefix.
It does not remove `~/.option-berth` runtime state (database, logs, runs); delete
that directory yourself after `oberth daemon stop` if you no longer need it.

The formula is regenerated from the latest engine release by the
`.github/workflows/update.yml` workflow. Run that workflow manually after a
release if you do not want to wait for the daily schedule.

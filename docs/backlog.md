# Open work and acceptance checklist

English | [中文](backlog.zh.md)

Use the current [CLI reference](cli.md) and actual help to determine implemented behavior, not an old plan. Earlier statements that cold scenarios and a no-skill arm did not exist are obsolete; see the [benchmark methodology](agent/agent-eval.md). The [old planning snapshot](agent/history/backlog-20261114.md) is retained only for traceability.

## Evidence to keep obtaining

| Area | Completion evidence |
| --- | --- |
| Coding-agent usability | Complete inputs and outputs for the named provider, scenario and revision; one success does not cover other agents |
| Comparative/performance claims | Controlled arms, repeated runs and intervals; failed preflight cannot produce a success table |
| Platform support | Native tests on each target; cross-builds do not replace Windows/Linux execution |
| Observation and event recovery | Successful system scans, real changes and cursor replay; a restricted-sandbox connection smoke is insufficient |
| Version switching and storage | Pinned released writer versus candidate ownership, failure release and read-path evidence |

These are acceptance requirements, not an assertion that a particular release omitted them: inspect its revision's CI and reports. [Verification](agent/validation.md) defines evidence levels; [release policy](agent/releasing.md) defines gates. This documentation task does not run real services or models or manufacture those results.

## Scope does not grow with the checklist

Open work does not authorize taking over external services, editing orchestrator settings automatically or approving model suggestions for a person. Missing observation permission stays unknown; do not bypass the sandbox. Application data deletion remains a person's choice.

The agent skill lives in its own repository. Protocol changes require coordination, not weakening this repository's tests to pretend the other component has adapted.

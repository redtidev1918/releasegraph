# The plan contract

A plan answers one question: **of everything this graph declares, what may start
now, what is waiting, and what needs nothing at all?** It is computed from fresh
state every time and never stored, so a stale plan cannot exist.

`schemas/plan-v1.json` describes the JSON that
`.github/workflows/reusable-readonly-plan.yml` consumes in production. That
workflow is the reason the shape is a contract rather than an implementation
detail.

## Wire format

```bash
releasegraph plan --graph release-graph.yml --state health.json --output json
releasegraph plan --graph release-graph.yml --live --output json
```

Both produce the same shape, wrapped in the CLI envelope:

```json
{
  "schemaVersion": 1,
  "releasegraphVersion": "0.1.0-go-readonly",
  "generatedAt": "2026-01-01T00:00:00Z",
  "data": {
    "ready": ["cli", "web"],
    "blocked": { "deploy": ["cli", "web"] },
    "noop": ["core"]
  }
}
```

| Field | Meaning |
|---|---|
| `ready` | Dependencies satisfied, and the project is not `HEALTHY`: work may start. |
| `blocked` | Project id -> the dependency ids whose condition is unsatisfied. |
| `noop` | Already `HEALTHY`. A released version is never re-released for its own sake. |

`testdata/plan/graph-plan.json` is a committed fixture produced by the real
binary, and `tests/test_plan_schema.py` validates it against the schema. The
fixture is the evidence that the schema describes what the tool emits, not what
someone believed it emits.

## Scope: one verb, two payloads

`releasegraph plan` has two forms with **incompatible** payloads that share a
property name:

| Form | `blocked` | Also emits |
|---|---|---|
| `plan --graph` (the graph view) | object: project id -> blocking ids | — |
| `plan --path` (the repository plan) | array of ids | `nodes` |

`schemas/plan-v1.json` describes **only the graph view**, because that is the one
with a production consumer. It is deliberately narrow:
`RepositoryPlanIsOutOfScopeTest` feeds it a repository-plan payload and requires
validation to **fail**, so the scope boundary is enforced rather than asserted in
prose. When the repository plan needs a contract, it gets its own schema — one
schema cannot honestly cover two payloads that disagree about a field's type.

## The state input

`--state <file>` maps a project id to that node's current state. It accepts the
**full** `internal/domain.Health` vocabulary, which is wider than the
repository-health vocabulary in [health](health.md):

```
HEALTHY READY RUNNING BLOCKED RECOVERABLE DEGRADED BROKEN NOOP
UNMANAGED NO_RELEASE NEEDS_REVIEW ACK_PENDING WAIVED
```

Wider is correct here, because the file describes a *node's* state, and a node
can be `READY` or `ACK_PENDING` — values that would be nonsense as repository
health. `testdata/graph/diamond-health.json` uses `READY`, which is why the
wider vocabulary is the tested one.

Only `HEALTHY` means "nothing to do". Every other value, including one that is
not in the list at all, makes the project a planning candidate: the loader does
not validate, so an unrecognised value plans work instead of silently skipping
it. That is the safe direction to fail in.

## Reading a failure

A failing plan writes nothing to stdout in JSON mode. It exits non-zero and puts
human-readable text such as `ERROR GRAPH_ERROR: cycle: a -> c -> b -> a` on
stderr, which is why the workflow's `cat release-plan.json` never shows a
half-written document.

The envelope type also declares an `error` field. **No command sets it today**,
and the schema says so in the field's own description rather than implying a
contract that does not exist. `test_plan_schema.py` fails if a command starts
emitting it, so the prose has to change with the code.

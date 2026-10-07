# Recover an interrupted draft after a workflow fix

The default repair still runs at the existing release tag. If the failure was
in the release workflow itself, review and merge the infrastructure fix first.
The repository's workflow_dispatch must forward the optional `source_ref` input
to the updated immutable ReleaseGraph reusable workflow.

Run `provider inspect` first, then plan:

```
releasegraph provider repair --repo owner/name --version X --resume-draft --scope fleet
```

The plan dispatches the current default branch's workflow, while every source
checkout builds the inspected original release commit. It requires a draft,
an existing tag matching the expected release commit, a failed/cancelled release
run at that commit, and no active non-PR release runs. Unknown or conflicting
tag targets and accepted historical waivers are refused. No tag is moved.
Pass `--apply` only after reviewing the plan; dispatch revalidates the guards.

The control-plane Provider operations workflow exposes `resume-draft` for
Linux execution with its explicit fleet credential. It inspects and plans
before applying. Existing automatic release and repair behavior is unchanged.

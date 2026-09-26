package rollout

import (
	"sort"
	"strings"
)

// GitHub token permission levels, lowest first. A scope is either absent, read,
// or write; a workflow file spells the absent case as "none" or by leaving the
// scope out.
const (
	PermissionNone  = "none"
	PermissionRead  = "read"
	PermissionWrite = "write"
)

// PermissionGrant maps a token scope (contents, id-token, ...) to a level.
type PermissionGrant map[string]string

// grantableScopes are the scopes a workflow may set a level for. They expand the
// read-all and write-all shorthands. metadata is deliberately absent: it is
// always read-only, so it can be neither requested nor granted at write level.
var grantableScopes = []string{
	"actions", "attestations", "checks", "contents", "deployments", "discussions",
	"id-token", "issues", "models", "packages", "pages", "pull-requests",
	"repository-projects", "security-events", "statuses",
}

func levelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case PermissionWrite:
		return 2
	case PermissionRead:
		return 1
	default:
		return 0
	}
}

// level reports the level a grant sets for one scope: a scope the grant does not
// mention grants nothing.
func (g PermissionGrant) level(scope string) string {
	if level, ok := g[scope]; ok {
		return level
	}
	return PermissionNone
}

// DefaultWorkflowPermissions is GitHub's default token permission set — read for
// every scope, write for none. A caller that declares no permissions block hands
// the called workflow exactly this, so a called job requesting a write scope
// fails the whole run at startup.
func DefaultWorkflowPermissions() PermissionGrant { return expanded(PermissionRead) }

func expanded(level string) PermissionGrant {
	grant := make(PermissionGrant, len(grantableScopes))
	for _, scope := range grantableScopes {
		grant[scope] = level
	}
	return grant
}

// union merges grants, keeping the highest level declared for each scope.
func union(grants ...PermissionGrant) PermissionGrant {
	merged := PermissionGrant{}
	for _, grant := range grants {
		for scope, level := range grant {
			if levelRank(level) > levelRank(merged[scope]) {
				merged[scope] = level
			}
		}
	}
	return merged
}

// RequestedPermissions returns every permission a called workflow asks its
// caller to grant.
//
// GitHub validates this when the run is created — before any job exists, so a
// job that would have been skipped still counts — and a reusable workflow can
// never elevate past its caller. Top-level and job-level blocks are therefore
// unioned: that union is a superset of what any single job needs, and a superset
// is the safe direction for a pre-flight check.
func RequestedPermissions(called []byte) PermissionGrant {
	blocks := permissionBlocks(scanWorkflow(called))
	grants := make([]PermissionGrant, 0, len(blocks))
	for _, block := range blocks {
		grants = append(grants, block.grant)
	}
	return union(grants...)
}

// GrantOf returns the permissions a release caller grants to the workflow it
// calls, and whether it declares any at all.
//
// Job-level permissions replace the workflow-level ones for that job, so the
// calling job's own block wins; without one the workflow-level block applies.
// When the caller declares nothing, the repository default applies — which this
// function cannot read, so the caller must fall back to
// DefaultWorkflowPermissions.
func GrantOf(caller []byte) (PermissionGrant, bool) {
	lines := scanWorkflow(caller)
	blocks := permissionBlocks(lines)
	grants := []PermissionGrant{}
	for _, job := range callerJobs(lines) {
		grant := blockIn(blocks, job[0], job[1])
		if grant == nil {
			grant = workflowLevelBlock(blocks)
		}
		if grant != nil {
			grants = append(grants, grant)
		}
	}
	if len(grants) == 0 {
		return PermissionGrant{}, false
	}
	return union(grants...), true
}

// MissingPermissions returns, sorted, the scopes a caller does not grant at the
// level the called workflow requests.
func MissingPermissions(granted, requested PermissionGrant) []string {
	missing := []string{}
	for scope, required := range requested {
		if levelRank(required) > levelRank(granted.level(scope)) {
			missing = append(missing, scope)
		}
	}
	sort.Strings(missing)
	return missing
}

// PermissionGaps reports the scopes a release caller must grant for the target
// version's reusable workflow, or the run fails at startup with zero jobs.
func PermissionGaps(caller []byte, requested PermissionGrant) []string {
	granted, declared := GrantOf(caller)
	if !declared {
		granted = DefaultWorkflowPermissions()
	}
	return MissingPermissions(granted, requested)
}

// workflowLine is one significant line of a workflow file.
type workflowLine struct {
	line   int
	indent int
	text   string
}

// scanWorkflow drops blank lines and comments and records indentation. Cutting
// at "#" is safe for the keys this file reads: a permission scope, a job name,
// and a uses: value never contain one.
func scanWorkflow(workflow []byte) []workflowLine {
	lines := []workflowLine{}
	for i, raw := range strings.Split(string(workflow), "\n") {
		if idx := strings.Index(raw, "#"); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		// YAML forbids tabs as indentation, so counting spaces is exact.
		lines = append(lines, workflowLine{
			line:   i + 1,
			indent: len(raw) - len(strings.TrimLeft(raw, " ")),
			text:   strings.TrimSpace(raw),
		})
	}
	return lines
}

// permissionBlock is one `permissions:` key together with the grant it declares.
type permissionBlock struct {
	index  int // position in the scanned lines, for job containment
	indent int
	grant  PermissionGrant
}

// permissionBlocks finds every permissions block in a workflow file, wherever it
// appears: GitHub allows one at the top level and one per job.
func permissionBlocks(lines []workflowLine) []permissionBlock {
	blocks := []permissionBlock{}
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i].text, "permissions:") {
			continue
		}
		grant, next := parsePermissionGrant(lines, i)
		blocks = append(blocks, permissionBlock{index: i, indent: lines[i].indent, grant: grant})
		i = next - 1
	}
	return blocks
}

// parsePermissionGrant parses the permissions key at lines[i] and returns the
// grant with the index of the first line after the block.
func parsePermissionGrant(lines []workflowLine, i int) (PermissionGrant, int) {
	value := strings.TrimSpace(strings.TrimPrefix(lines[i].text, "permissions:"))
	if value != "" {
		return parsePermissionValue(value), i + 1
	}
	grant := PermissionGrant{}
	childIndent := -1
	j := i + 1
	for ; j < len(lines); j++ {
		if lines[j].indent <= lines[i].indent {
			break
		}
		if childIndent < 0 {
			childIndent = lines[j].indent
		}
		if lines[j].indent != childIndent {
			continue
		}
		scope, level, ok := strings.Cut(lines[j].text, ":")
		if !ok {
			continue
		}
		grant[strings.TrimSpace(scope)] = strings.TrimSpace(level)
	}
	return grant, j
}

// parsePermissionValue parses a permissions value written on one line: a
// shorthand (read-all, write-all, none, {}) or a flow mapping.
func parsePermissionValue(value string) PermissionGrant {
	switch strings.ToLower(value) {
	case "read-all":
		return expanded(PermissionRead)
	case "write-all":
		return expanded(PermissionWrite)
	case "none":
		return PermissionGrant{}
	}
	grant := PermissionGrant{}
	for _, pair := range strings.Split(strings.Trim(value, "{}"), ",") {
		scope, level, ok := strings.Cut(pair, ":")
		if !ok {
			continue
		}
		grant[strings.TrimSpace(scope)] = strings.TrimSpace(level)
	}
	return grant
}

// callerJobs returns the line ranges of the jobs that call the ReleaseGraph
// release workflow.
func callerJobs(lines []workflowLine) [][2]int {
	start := indexOfKey(lines, "jobs:")
	if start < 0 {
		return nil
	}
	jobIndent := -1
	for _, line := range lines[start+1:] {
		jobIndent = line.indent
		break
	}
	if jobIndent <= lines[start].indent {
		return nil
	}
	starts := []int{}
	for i := start + 1; i < len(lines); i++ {
		if lines[i].indent == jobIndent && strings.HasSuffix(lines[i].text, ":") {
			starts = append(starts, i)
		}
	}
	ranges := [][2]int{}
	for n, jobStart := range starts {
		end := len(lines)
		if n+1 < len(starts) {
			end = starts[n+1]
		}
		for i := jobStart + 1; i < end; i++ {
			if isReleaseCall(lines[i].text) {
				ranges = append(ranges, [2]int{jobStart, end})
				break
			}
		}
	}
	return ranges
}

func indexOfKey(lines []workflowLine, key string) int {
	for i, line := range lines {
		if line.text == key {
			return i
		}
	}
	return -1
}

func blockIn(blocks []permissionBlock, start, end int) PermissionGrant {
	for _, block := range blocks {
		if block.index > start && block.index < end {
			return block.grant
		}
	}
	return nil
}

func workflowLevelBlock(blocks []permissionBlock) PermissionGrant {
	for _, block := range blocks {
		if block.indent == 0 {
			return block.grant
		}
	}
	return nil
}

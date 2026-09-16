package rollout

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/redtidev1918/releasegraph/internal/policy"
)

// MetadataAsset is the compatibility document published with every ReleaseGraph
// release.
const MetadataAsset = "RELEASEGRAPH-METADATA.json"

// Compatibility is what a ReleaseGraph release states about itself: which
// interfaces it exposes, which policy schema it can read, and which capabilities
// it changes. Rollout uses it so a small fix does not touch the whole fleet.
type Compatibility struct {
	Version              string   `json:"version"`
	WorkflowAPI          int      `json:"workflow_api"`
	PolicySchema         int      `json:"policy_schema"`
	MinimumPolicySchema  int      `json:"minimum_policy_schema"`
	Breaking             bool     `json:"breaking"`
	AffectedCapabilities []string `json:"affected_capabilities"`
}

// ParseCompatibility decodes a compatibility document.
func ParseCompatibility(raw []byte) (Compatibility, error) {
	var compat Compatibility
	if err := json.Unmarshal(raw, &compat); err != nil {
		return compat, fmt.Errorf("parse %s: %w", MetadataAsset, err)
	}
	return compat, nil
}

// CompatStatus classifies one repository against a ReleaseGraph release.
const (
	CompatUnaffected         = "UNAFFECTED"
	CompatUpgradeRecommended = "UPGRADE_RECOMMENDED"
	CompatUpgradeRequired    = "UPGRADE_REQUIRED"
	CompatMigrationRequired  = "MIGRATION_REQUIRED"
	CompatIncompatible       = "INCOMPATIBLE"
)

// Affected decides whether a repository with these capabilities is touched by a
// ReleaseGraph release, and why.
//
// Rules, in order:
//  1. A breaking release that the repository's policy schema cannot express is
//     INCOMPATIBLE.
//  2. A release that raises the minimum policy schema requires migration.
//  3. An empty affected_capabilities means "unknown scope": treat everything as
//     affected (conservative, never silently skip a repository).
//  4. Otherwise the repository is affected only if it declares at least one
//     affected capability.
func Affected(capabilities []string, compat Compatibility, policySchema int) (string, string) {
	if compat.Breaking && compat.MinimumPolicySchema > policySchema {
		return CompatIncompatible, fmt.Sprintf("policy schema %d cannot express this release (needs %d)",
			policySchema, compat.MinimumPolicySchema)
	}
	if compat.MinimumPolicySchema > policySchema {
		return CompatMigrationRequired, fmt.Sprintf("policy schema %d must be migrated to %d",
			policySchema, compat.MinimumPolicySchema)
	}
	if len(compat.AffectedCapabilities) == 0 {
		return CompatUpgradeRecommended, "release does not declare affected capabilities; treating every repository as affected"
	}
	declared := map[string]bool{}
	for _, name := range capabilities {
		declared[name] = true
	}
	affected := []string{}
	for _, name := range compat.AffectedCapabilities {
		if declared[name] {
			affected = append(affected, name)
		}
	}
	if len(affected) == 0 {
		return CompatUnaffected, fmt.Sprintf("repository declares none of %v", compat.AffectedCapabilities)
	}
	sort.Strings(affected)
	return CompatUpgradeRecommended, fmt.Sprintf("affects declared capability/capabilities: %v", affected)
}

// CapabilityNames exposes a policy's capability identifiers.
func CapabilityNames(caps policy.Capabilities) []string { return caps.Names() }

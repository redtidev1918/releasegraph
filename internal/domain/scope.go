package domain

// ExecutionScope states which authority a ReleaseGraph process is running with.
// It is part of the domain so that a caller cannot "convention" its way into
// cross-repository writes: the adapters enforce the scope themselves.
type ExecutionScope string

const (
	// ScopeRepository is the normal release path. It is bound to exactly one
	// repository and may not touch any other.
	ScopeRepository ExecutionScope = "repository"
	// ScopeFleet is the central control plane. It may inspect and dispatch
	// across the managed repositories listed in fleet.yaml.
	ScopeFleet ExecutionScope = "fleet"
)

// CredentialClass names the credential a scope is entitled to use. Repository
// and fleet credentials are different concepts and must not be interchangeable.
type CredentialClass string

const (
	// CredentialRepository is the repository-scoped token (GITHUB_TOKEN).
	CredentialRepository CredentialClass = "repo"
	// CredentialFleet is the cross-repository token (RELEASEGRAPH_FLEET_TOKEN).
	CredentialFleet CredentialClass = "fleet"
	// CredentialNone is used for read-only public operations.
	CredentialNone CredentialClass = "none"
)

// ExecutionContext is the answer to "where am I running and what may I touch?".
// Commands that act on release state carry one; the adapters refuse work that
// falls outside it.
type ExecutionContext struct {
	// Scope is the authority this process runs under.
	Scope ExecutionScope `json:"scope"`
	// Repository is the single repository a repository-scoped process is bound
	// to. Empty for fleet scope.
	Repository string `json:"repository,omitempty"`
	// Actor records who asked (agent, workflow, operator) for the audit trail.
	Actor string `json:"actor,omitempty"`
	// CredentialClass records which credential the scope is entitled to use.
	CredentialClass CredentialClass `json:"credentialClass"`
}

// Allows reports whether name may be operated on in this context.
func (c ExecutionContext) Allows(name string) bool {
	if c.Scope == ScopeFleet {
		return true
	}
	return name != "" && name == c.Repository
}

// Describe renders the context for diagnostics.
func (c ExecutionContext) Describe() string {
	switch c.Scope {
	case ScopeFleet:
		return "scope=fleet credential=" + string(c.CredentialClass)
	case ScopeRepository:
		return "scope=repository repo=" + c.Repository + " credential=" + string(c.CredentialClass)
	default:
		return "scope=<unset>"
	}
}

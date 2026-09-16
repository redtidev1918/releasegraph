package domain

type Health string

const (
	HealthHealthy     Health = "HEALTHY"
	HealthReady       Health = "READY"
	HealthRunning     Health = "RUNNING"
	HealthBlocked     Health = "BLOCKED"
	HealthRecoverable Health = "RECOVERABLE"
	HealthDegraded    Health = "DEGRADED"
	HealthBroken      Health = "BROKEN"
	HealthNoop        Health = "NOOP"
	HealthUnmanaged   Health = "UNMANAGED"
	HealthNoRelease   Health = "NO_RELEASE"
	HealthNeedsReview Health = "NEEDS_REVIEW"
	// HealthACKPending means the actual release transaction is healthy but the
	// version provider has not acknowledged it yet.
	HealthACKPending Health = "ACK_PENDING"
	// HealthWaived marks a historical version a human explicitly waived after
	// deciding its release must not be fabricated retroactively.
	HealthWaived Health = "WAIVED"
)

type NodeKind string

const (
	NodeKindRelease       NodeKind = "release"
	NodeKindDeploy        NodeKind = "deploy"
	NodeKindReconcileOnly NodeKind = "reconcile-only"
)

type DependencyCondition string

const (
	ConditionHealthy  DependencyCondition = "healthy"
	ConditionComplete DependencyCondition = "complete"
)

type Repository struct {
	Owner string `json:"owner" yaml:"owner"`
	Name  string `json:"name" yaml:"name"`
}

func (r Repository) FullName() string { return r.Owner + "/" + r.Name }

type Version string
type Commit string

type Project struct {
	ID        string       `json:"id" yaml:"id"`
	Kind      NodeKind     `json:"kind" yaml:"kind"`
	Repo      Repository   `json:"repo" yaml:"repo"`
	DependsOn []Dependency `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`
}

type Dependency struct {
	ID        string              `json:"id" yaml:"id"`
	Condition DependencyCondition `json:"condition" yaml:"condition"`
}

type ReleaseGraph struct {
	APIVersion string             `json:"apiVersion" yaml:"apiVersion"`
	Owner      string             `json:"owner,omitempty" yaml:"owner,omitempty"`
	Projects   map[string]Project `json:"projects" yaml:"projects"`
}

type ReleaseAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

type Release struct {
	Tag    string         `json:"tag"`
	Draft  bool           `json:"draft"`
	Pre    bool           `json:"prerelease"`
	Latest bool           `json:"latest"`
	Commit Commit         `json:"commit"`
	Assets []ReleaseAsset `json:"assets,omitempty"`
	URL    string         `json:"url,omitempty"`
}

type Registry struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Version  string `json:"version,omitempty"`
	Healthy  bool   `json:"healthy"`
}

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type DesiredState struct {
	Version Version `json:"version"`
}

type ActualState struct {
	Version    Version    `json:"version,omitempty"`
	Commit     Commit     `json:"commit,omitempty"`
	Release    *Release   `json:"release,omitempty"`
	Registries []Registry `json:"registries,omitempty"`
	Health     Health     `json:"health"`
}

type NodePlan struct {
	ID         string        `json:"id"`
	Kind       NodeKind      `json:"kind"`
	Repository string        `json:"repository"`
	Health     Health        `json:"health"`
	Desired    *DesiredState `json:"desired,omitempty"`
	Actual     *ActualState  `json:"actual,omitempty"`
	BlockedBy  []string      `json:"blockedBy,omitempty"`
	Failures   []Failure     `json:"failures,omitempty"`
}

type Plan struct {
	Ready   []string   `json:"ready"`
	Blocked []string   `json:"blocked"`
	Noop    []string   `json:"noop"`
	Nodes   []NodePlan `json:"nodes"`
}

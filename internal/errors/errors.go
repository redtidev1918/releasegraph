package errors

import stderrors "errors"

type ErrorKind string

const (
	Transient          ErrorKind = "TRANSIENT_ERROR"
	Authentication     ErrorKind = "AUTHENTICATION_ERROR"
	Permission         ErrorKind = "PERMISSION_ERROR"
	Build              ErrorKind = "BUILD_ERROR"
	Policy             ErrorKind = "POLICY_ERROR"
	Graph              ErrorKind = "GRAPH_ERROR"
	TagConflict        ErrorKind = "TAG_CONFLICT_ERROR"
	Asset              ErrorKind = "ASSET_ERROR"
	RegistryConflict   ErrorKind = "REGISTRY_CONFLICT_ERROR"
	VersionConflict    ErrorKind = "VERSION_CONFLICT_ERROR"
	InvariantViolation ErrorKind = "INVARIANT_VIOLATION"
	NotFound           ErrorKind = "NOT_FOUND"
	// ScopeViolation means an operation targeted a repository outside the
	// execution scope it was running under.
	ScopeViolation ErrorKind = "SCOPE_VIOLATION"
	// FleetCredentialRequired means fleet-scoped work ran without a fleet
	// credential. It is detected before any API call returns 403.
	FleetCredentialRequired ErrorKind = "FLEET_CREDENTIAL_REQUIRED"
	// PlanStale means the remote state changed after a plan was produced.
	// Applying a stale plan could mutate state that no longer matches it.
	PlanStale ErrorKind = "PLAN_STALE"
)

type Error struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func New(kind ErrorKind, message string) error { return &Error{Kind: kind, Message: message} }
func Wrap(kind ErrorKind, message string, cause error) error {
	return &Error{Kind: kind, Message: message, Cause: cause}
}

func IsKind(err error, kind ErrorKind) bool {
	var typed *Error
	return stderrors.As(err, &typed) && typed.Kind == kind
}

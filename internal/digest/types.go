package digest

import (
	"context"
	"errors"
	"time"
)

const (
	defaultCustomerBudget = 30 * time.Second

	WarningTimeout = "timeout"
	WarningOOM     = "oom"
	WarningError   = "error"
	WarningBudget  = "budget"
)

var (
	ErrFunctionTimeout = errors.New("digest function timeout")
	ErrFunctionOOM     = errors.New("digest function oom")
)

type SectionSource string

const (
	SectionSourceFirstParty SectionSource = "first-party"
	SectionSourceCustomer   SectionSource = "customer"
)

type Window struct {
	WorkspaceID string
	Covers      string
	Date        time.Time
	From        time.Time
	To          time.Time
	GeneratedAt time.Time
	TotalEvents int
}

type DigestBullet struct {
	Text          string
	CanonicalPath string
	OccurredAt    time.Time
}

type DigestSection struct {
	Provider string
	Bullets  []DigestBullet
	Source   SectionSource
}

type DigestWarning struct {
	Function string
	Reason   string
}

type Digest struct {
	Window   Window
	Sections []DigestSection
	Warnings []DigestWarning
}

type FunctionRef struct {
	Name        string
	FunctionID  string
	WorkspaceID string
	ContentHash string
}

type DigestContext struct {
	WorkspaceID string
	FunctionID  string
	Window      Window
}

// FunctionRegistry is implemented by the daemon-side custom-function cache.
type FunctionRegistry interface {
	ListActiveDigestFunctions(ctx context.Context, workspaceID string) ([]FunctionRef, error)
}

// CustomFunctionRunner is implemented by internal/wasmrun without exposing the
// digest package to wazero or QuickJS details.
type CustomFunctionRunner interface {
	RunDigestFunction(ctx context.Context, fn FunctionRef, input DigestContext) (*DigestSection, error)
}

type Options struct {
	Registry       FunctionRegistry
	Runner         CustomFunctionRunner
	CustomerBudget time.Duration
	Now            func() time.Time
}

type Generator struct {
	registry FunctionRegistry
	runner   CustomFunctionRunner
	budget   time.Duration
	now      func() time.Time
}

type GenerateRequest struct {
	Window             Window
	FirstPartySections []DigestSection
}

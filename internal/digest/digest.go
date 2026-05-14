package digest

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

func NewGenerator(options Options) *Generator {
	budget := options.CustomerBudget
	if budget <= 0 {
		budget = defaultCustomerBudget
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}

	return &Generator{
		registry: options.Registry,
		runner:   options.Runner,
		budget:   budget,
		now:      now,
	}
}

func (g *Generator) Generate(ctx context.Context, req GenerateRequest) (Digest, error) {
	d := Digest{
		Window:   req.Window,
		Sections: normalizeFirstPartySections(req.FirstPartySections),
	}
	if g == nil || g.registry == nil || g.runner == nil || strings.TrimSpace(req.Window.WorkspaceID) == "" {
		d.Sections = sortSections(d.Sections)
		return d, nil
	}

	functions, err := g.registry.ListActiveDigestFunctions(ctx, req.Window.WorkspaceID)
	if err != nil {
		return Digest{}, err
	}
	functions = activeFunctionsForWorkspace(functions, req.Window.WorkspaceID)
	sort.SliceStable(functions, func(i, j int) bool {
		return functions[i].Name < functions[j].Name
	})

	startedAt := g.now()
	for _, fn := range functions {
		elapsed := g.now().Sub(startedAt)
		remaining := g.budget - elapsed
		if remaining <= 0 {
			d.Warnings = append(d.Warnings, DigestWarning{Function: fn.Name, Reason: WarningBudget})
			continue
		}

		runCtx, cancel := context.WithTimeout(ctx, remaining)
		section, runErr := g.runFunction(runCtx, fn, req.Window)
		cancel()

		if runErr != nil {
			d.Warnings = append(d.Warnings, DigestWarning{Function: fn.Name, Reason: classifyRunError(runErr)})
			continue
		}
		if section == nil {
			continue
		}
		section.Source = SectionSourceCustomer
		d.Sections = append(d.Sections, *section)
	}

	d.Sections = sortSections(d.Sections)
	d.Warnings = sortWarnings(d.Warnings)
	return d, nil
}

func (g *Generator) runFunction(ctx context.Context, fn FunctionRef, window Window) (section *DigestSection, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errors.New("digest function panic")
			section = nil
		}
	}()

	return g.runner.RunDigestFunction(ctx, fn, DigestContext{
		WorkspaceID: window.WorkspaceID,
		FunctionID:  fn.FunctionID,
		Window:      window,
	})
}

func normalizeFirstPartySections(sections []DigestSection) []DigestSection {
	normalized := make([]DigestSection, 0, len(sections))
	for _, section := range sections {
		section.Provider = strings.TrimSpace(section.Provider)
		if section.Provider == "" {
			continue
		}
		if section.Source == "" {
			section.Source = SectionSourceFirstParty
		}
		section.Bullets = normalizeBullets(section.Bullets)
		normalized = append(normalized, section)
	}
	return normalized
}

func normalizeBullets(bullets []DigestBullet) []DigestBullet {
	normalized := make([]DigestBullet, 0, len(bullets))
	for _, bullet := range bullets {
		bullet.Text = strings.TrimSpace(bullet.Text)
		bullet.CanonicalPath = strings.TrimSpace(bullet.CanonicalPath)
		if bullet.Text == "" {
			continue
		}
		normalized = append(normalized, bullet)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if !normalized[i].OccurredAt.Equal(normalized[j].OccurredAt) {
			return normalized[i].OccurredAt.Before(normalized[j].OccurredAt)
		}
		if normalized[i].CanonicalPath != normalized[j].CanonicalPath {
			return normalized[i].CanonicalPath < normalized[j].CanonicalPath
		}
		return normalized[i].Text < normalized[j].Text
	})
	return normalized
}

func activeFunctionsForWorkspace(functions []FunctionRef, workspaceID string) []FunctionRef {
	active := make([]FunctionRef, 0, len(functions))
	for _, fn := range functions {
		fn.Name = strings.TrimSpace(fn.Name)
		fn.FunctionID = strings.TrimSpace(fn.FunctionID)
		fn.WorkspaceID = strings.TrimSpace(fn.WorkspaceID)
		if fn.Name == "" || fn.FunctionID == "" {
			continue
		}
		if fn.WorkspaceID != "" && fn.WorkspaceID != workspaceID {
			continue
		}
		active = append(active, fn)
	}
	return active
}

func classifyRunError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrFunctionTimeout):
		return WarningTimeout
	case errors.Is(err, ErrFunctionOOM):
		return WarningOOM
	default:
		return WarningError
	}
}

func sortSections(sections []DigestSection) []DigestSection {
	sorted := make([]DigestSection, 0, len(sections))
	for _, section := range normalizeFirstPartySections(sections) {
		if section.Source == SectionSourceCustomer {
			section.Bullets = normalizeBullets(section.Bullets)
		}
		sorted = append(sorted, section)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		left := sourceRank(sorted[i].Source)
		right := sourceRank(sorted[j].Source)
		if left != right {
			return left < right
		}
		return sorted[i].Provider < sorted[j].Provider
	})
	return sorted
}

func sourceRank(source SectionSource) int {
	if source == SectionSourceCustomer {
		return 1
	}
	return 0
}

func sortWarnings(warnings []DigestWarning) []DigestWarning {
	sorted := make([]DigestWarning, 0, len(warnings))
	for _, warning := range warnings {
		warning.Function = strings.TrimSpace(warning.Function)
		warning.Reason = strings.TrimSpace(warning.Reason)
		if warning.Function == "" || warning.Reason == "" {
			continue
		}
		sorted = append(sorted, warning)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Function != sorted[j].Function {
			return sorted[i].Function < sorted[j].Function
		}
		return sorted[i].Reason < sorted[j].Reason
	})
	return sorted
}

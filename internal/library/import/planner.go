package libraryimport

import (
	"context"
	"fmt"
	"strings"

	"github.com/jamie75/librarr/internal/library"
)

type ImportPlanner struct {
	catalog             Catalog
	metadataResolver    *MetadataResolver
	bookResolver        *BookResolver
	editionResolver     *EditionResolver
	contributorResolver *ContributorResolver
	duplicateDetector   *DuplicateDetector
	filePlanner         *FilePlanner
}

type batchState struct {
	plans []ImportPlan
}

func NewImportPlanner(catalog Catalog) *ImportPlanner {
	return &ImportPlanner{
		catalog:             catalog,
		metadataResolver:    &MetadataResolver{},
		bookResolver:        &BookResolver{catalog: catalog},
		editionResolver:     &EditionResolver{catalog: catalog},
		contributorResolver: &ContributorResolver{catalog: catalog},
		duplicateDetector:   &DuplicateDetector{catalog: catalog},
		filePlanner:         &FilePlanner{catalog: catalog},
	}
}

func (p *ImportPlanner) Plan(ctx context.Context, pc PlanningContext) (ImportResult, error) {
	candidates, err := discoverCandidates(ctx, pc)
	if err != nil {
		return ImportResult{}, err
	}
	return p.PlanCandidates(ctx, pc, candidates)
}

func (p *ImportPlanner) PlanCandidates(ctx context.Context, pc PlanningContext, candidates []ImportCandidate) (ImportResult, error) {
	result := ImportResult{
		Plans:  make([]ImportPlan, 0, len(candidates)),
		Counts: map[string]int{},
	}
	state := &batchState{}
	for _, candidate := range candidates {
		plan, err := p.planCandidate(ctx, pc, candidate, state)
		if err != nil {
			return ImportResult{}, fmt.Errorf("plan candidate %q: %w", candidate.Path, err)
		}
		result.Plans = append(result.Plans, plan)
		state.plans = append(state.plans, plan)
		result.Counts[string(plan.Disposition)]++
	}
	return result, nil
}

func (p *ImportPlanner) planCandidate(ctx context.Context, pc PlanningContext, candidate ImportCandidate, state *batchState) (ImportPlan, error) {
	if err := p.metadataResolver.Resolve(ctx, &candidate); err != nil {
		return ImportPlan{}, err
	}
	// Physical identity is stronger than metadata identity. Check it before
	// book resolution so an already-imported file cannot be reclassified as a
	// title/author conflict on a later scan.
	duplicate, err := p.duplicateDetector.Detect(ctx, candidate)
	if err != nil {
		return ImportPlan{}, err
	}
	if duplicate != nil {
		plan := ImportPlan{Candidate: candidate, File: FileDecision{
			Action:     FileActionIgnoreDuplicate,
			Existing:   duplicate.File,
			Confidence: library.ConfidenceExact,
			Evidence: []PlanningEvidence{{
				Signal:      "duplicate_" + duplicate.Reason,
				Value:       candidate.Path,
				Source:      "catalog",
				Confidence:  library.ConfidenceExact,
				Explanation: "Existing file already matches this candidate",
			}},
		}}
		plan.Disposition = DispositionIgnoreDuplicate
		if duplicate.File != nil && duplicate.File.BookID != 0 {
			if book, lookupErr := p.catalog.GetBook(ctx, duplicate.File.BookID); lookupErr == nil {
				plan.Book = ResolvedBook{Action: BookActionReuse, Existing: book, Confidence: library.ConfidenceExact}
			}
		}
		return plan, nil
	}

	book, err := p.bookResolver.Resolve(ctx, pc, candidate, state)
	if err != nil {
		return ImportPlan{}, err
	}
	plan := ImportPlan{
		Candidate: candidate,
		Book:      book,
	}

	if book.Action == BookActionConflict {
		plan.Disposition = DispositionConflict
		plan.Evidence = append(plan.Evidence, book.Evidence...)
		return plan, nil
	}
	if book.Action == BookActionNeedsManualReview {
		plan.Disposition = DispositionNeedsManualReview
		plan.Evidence = append(plan.Evidence, book.Evidence...)
		return plan, nil
	}

	edition, err := p.editionResolver.Resolve(ctx, candidate, book, state)
	if err != nil {
		return ImportPlan{}, err
	}
	plan.Edition = edition
	plan.Evidence = append(plan.Evidence, book.Evidence...)
	plan.Evidence = append(plan.Evidence, edition.Evidence...)

	if edition.Action == EditionActionConflict {
		plan.Disposition = DispositionConflict
		return plan, nil
	}
	if edition.Action == EditionActionNeedsManualReview {
		plan.Disposition = DispositionNeedsManualReview
		return plan, nil
	}

	contributors, err := p.contributorResolver.Resolve(ctx, candidate, book, edition)
	if err != nil {
		return ImportPlan{}, err
	}
	plan.Contributors = contributors
	for _, contributor := range contributors {
		plan.Evidence = append(plan.Evidence, contributor.Evidence...)
	}

	fileDecision, err := p.filePlanner.Resolve(ctx, candidate, book, edition, duplicate, state)
	if err != nil {
		return ImportPlan{}, err
	}
	plan.File = fileDecision
	plan.Evidence = append(plan.Evidence, fileDecision.Evidence...)

	switch fileDecision.Action {
	case FileActionIgnoreDuplicate:
		plan.Disposition = DispositionIgnoreDuplicate
	case FileActionConflict:
		plan.Disposition = DispositionConflict
	case FileActionNeedsManualReview:
		plan.Disposition = DispositionNeedsManualReview
	case FileActionAttachNewFormat:
		plan.Disposition = DispositionAttachNewFormat
	default:
		switch {
		case book.Action == BookActionCreate:
			plan.Disposition = DispositionCreateNewBook
		case edition.Action == EditionActionCreate:
			plan.Disposition = DispositionCreateEdition
		default:
			plan.Disposition = DispositionAttachNewFormat
		}
	}

	return plan, nil
}

func primaryContributorName(book *library.Book) string {
	if book == nil {
		return ""
	}
	for _, contributor := range book.Contributors {
		if isAuthorContributor(contributor) && !isUnknownAuthor(contributor.Name) {
			return contributor.Name
		}
	}
	for _, contributor := range book.Contributors {
		if isAuthorContributor(contributor) && strings.TrimSpace(contributor.Name) != "" {
			return contributor.Name
		}
	}
	for _, contributor := range book.Contributors {
		if strings.TrimSpace(contributor.Name) != "" && !isUnknownAuthor(contributor.Name) {
			return contributor.Name
		}
	}
	return ""
}

func isAuthorContributor(contributor library.Contributor) bool {
	if len(contributor.Roles) == 0 {
		return true
	}
	for _, role := range contributor.Roles {
		if role == library.RoleAuthor {
			return true
		}
	}
	return false
}

func isUnknownAuthor(value string) bool {
	switch library.ContributorMatchKey(value) {
	case "", "unknown", "unknown author", "n a", "na", "none":
		return true
	default:
		return false
	}
}

func (s *batchState) findPlannedBook(candidate ImportCandidate) *ImportPlan {
	titleKey := importTitleMatchKey(candidate.Metadata.SelectedTitle)
	authorKey := library.ContributorMatchKey(candidate.Metadata.SelectedAuthor)
	for i := range s.plans {
		plan := &s.plans[i]
		if plan.Disposition == DispositionConflict || plan.Disposition == DispositionNeedsManualReview || plan.Disposition == DispositionIgnoreDuplicate {
			continue
		}
		var plannedTitle, plannedAuthor string
		switch {
		case plan.Book.Existing != nil:
			plannedTitle = plan.Book.Existing.Title
			plannedAuthor = primaryContributorName(plan.Book.Existing)
		case plan.Book.Proposed != nil:
			plannedTitle = plan.Book.Proposed.Title
			plannedAuthor = firstContributorName(plan.Contributors)
		}
		if importTitleMatchKey(plannedTitle) == titleKey &&
			library.ContributorMatchKey(plannedAuthor) == authorKey &&
			plan.Candidate.MediaType == candidate.MediaType {
			return plan
		}
	}
	return nil
}

func firstContributorName(contributors []ResolvedContributor) string {
	for _, contributor := range contributors {
		if contributor.Existing != nil && strings.TrimSpace(contributor.Existing.Name) != "" {
			return contributor.Existing.Name
		}
		if contributor.Proposed != nil && strings.TrimSpace(contributor.Proposed.Name) != "" {
			return contributor.Proposed.Name
		}
	}
	return ""
}

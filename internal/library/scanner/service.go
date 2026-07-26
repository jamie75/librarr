package scanner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/librarr/internal/library"
	libraryimport "github.com/JeremiahM37/librarr/internal/library/import"
)

const maxCompletedJobs = 8

type Manager struct {
	catalog libraryimport.Catalog
	now     func() time.Time

	mu       sync.Mutex
	jobs     map[string]*Job
	order    []string
	activeID string
}

func NewManager(catalog libraryimport.Catalog) *Manager {
	return &Manager{
		catalog: catalog,
		now:     time.Now,
		jobs:    map[string]*Job{},
	}
}

type ActiveJobError struct {
	JobID string
}

func (e *ActiveJobError) Error() string {
	return "library scan already running"
}

func (m *Manager) Start(ctx context.Context, roots Roots) (*Job, error) {
	if m == nil || m.catalog == nil {
		return nil, fmt.Errorf("library scanner is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := m.now()
	job := &Job{
		ID:        newJobID(),
		Status:    StatusPending,
		StartedAt: now,
		Progress: Progress{
			Status:    StatusPending,
			StartedAt: now,
		},
	}

	m.mu.Lock()
	if m.activeID != "" {
		active := m.cloneJobLocked(m.activeID)
		m.mu.Unlock()
		if active != nil {
			return nil, &ActiveJobError{JobID: active.ID}
		}
		return nil, &ActiveJobError{}
	}
	m.jobs[job.ID] = job
	m.order = append(m.order, job.ID)
	m.activeID = job.ID
	m.pruneLocked()
	m.mu.Unlock()

	started := cloneJob(job)
	go m.run(context.Background(), job.ID, roots)
	return started, nil
}

func (m *Manager) Get(jobID string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.cloneJobLocked(jobID)
	return job, job != nil
}

func (m *Manager) Result(jobID string) (*Result, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil || job.Result == nil {
		return nil, false
	}
	result := *job.Result
	result.Candidates = append([]Candidate(nil), job.Result.Candidates...)
	result.Warnings = append([]Warning(nil), job.Result.Warnings...)
	return &result, true
}

func (m *Manager) run(ctx context.Context, jobID string, roots Roots) {
	result, err := m.scan(ctx, jobID, roots)
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil {
		return
	}
	completed := m.now()
	job.CompletedAt = &completed
	job.Progress.CompletedAt = &completed
	m.activeID = ""
	if err != nil {
		if errors.Is(err, context.Canceled) {
			job.Status = StatusCancelled
			job.Progress.Status = StatusCancelled
		} else {
			job.Status = StatusFailed
			job.Progress.Status = StatusFailed
			job.Error = err.Error()
		}
		return
	}
	job.Status = StatusCompleted
	job.Progress.Status = StatusCompleted
	job.Progress.CandidatesReady = len(result.Candidates)
	result.Status = StatusCompleted
	result.CompletedAt = &completed
	job.Result = result
}

func (m *Manager) scan(ctx context.Context, jobID string, roots Roots) (*Result, error) {
	m.update(jobID, func(p *Progress) {
		p.Status = StatusScanning
		p.CurrentPhase = "scanning"
	})

	items := make([]discoveredItem, 0)
	warnings := make([]Warning, 0)
	for _, root := range configuredRoots(roots) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		found, rootWarnings := m.scanRoot(ctx, jobID, root)
		items = append(items, found...)
		warnings = append(warnings, rootWarnings...)
	}

	m.update(jobID, func(p *Progress) {
		p.Status = StatusProcessingMetadata
		p.CurrentPhase = "processing_metadata"
		p.Warnings = append([]Warning(nil), warnings...)
	})

	supported := make([]libraryimport.ImportCandidate, 0)
	candidates := make([]Candidate, 0, len(items))
	indexByPath := map[string]int{}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := item.Candidate
		switch item.State {
		case ClassificationUnsupported, ClassificationUnreadable:
			candidates = append(candidates, candidate)
		default:
			indexByPath[candidate.Path] = len(candidates)
			candidates = append(candidates, candidate)
			supported = append(supported, item.ImportCandidate)
		}
		m.update(jobID, func(p *Progress) {
			p.FilesProcessed++
			p.CandidatesReady = len(candidates)
			p.CurrentPath = candidate.Path
		})
	}

	m.update(jobID, func(p *Progress) {
		p.Status = StatusClassifying
		p.CurrentPhase = "classifying"
	})

	if len(supported) > 0 {
		planner := libraryimport.NewImportPlanner(m.catalog)
		planned, err := planner.PlanCandidates(ctx, libraryimport.PlanningContext{
			Source: library.ImportSource{Name: "library_scan"},
			Now:    m.now(),
		}, supported)
		if err != nil {
			return nil, err
		}
		for _, plan := range planned.Plans {
			idx, ok := indexByPath[plan.Candidate.Path]
			if !ok {
				continue
			}
			applyPlan(&candidates[idx], plan)
		}
	}

	totals := countTotals(candidates)
	return &Result{
		JobID:      jobID,
		Status:     StatusCompleted,
		Totals:     totals,
		Candidates: candidates,
		Warnings:   warnings,
		StartedAt:  m.jobsStartedAt(jobID),
	}, nil
}

type rootConfig struct {
	MediaType library.MediaType
	Path      string
	Formats   map[string]struct{}
}

type discoveredState = Classification

type discoveredItem struct {
	State           discoveredState
	Candidate       Candidate
	ImportCandidate libraryimport.ImportCandidate
}

func configuredRoots(roots Roots) []rootConfig {
	return []rootConfig{
		{MediaType: library.MediaTypeEbook, Path: roots.EbookDir, Formats: extensions(".epub", ".pdf", ".mobi", ".azw3")},
		{MediaType: library.MediaTypeAudiobook, Path: roots.AudiobookDir, Formats: extensions(".m4b", ".mp3")},
		{MediaType: library.MediaTypeManga, Path: roots.MangaDir, Formats: extensions(".cbz", ".cbr", ".pdf")},
	}
}

func extensions(values ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func (m *Manager) scanRoot(ctx context.Context, jobID string, root rootConfig) ([]discoveredItem, []Warning) {
	cleanRoot := filepath.Clean(root.Path)
	if strings.TrimSpace(root.Path) == "" || cleanRoot == "." {
		return nil, []Warning{{MediaType: root.MediaType, Message: "Library folder is not configured"}}
	}
	info, err := os.Stat(cleanRoot)
	if err != nil {
		return nil, []Warning{{MediaType: root.MediaType, Path: cleanRoot, Message: "Folder not found"}}
	}
	if !info.IsDir() {
		return nil, []Warning{{MediaType: root.MediaType, Path: cleanRoot, Message: "Configured library folder is not a directory"}}
	}

	var items []discoveredItem
	var warnings []Warning
	err = filepath.WalkDir(cleanRoot, func(path string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			warnings = append(warnings, Warning{MediaType: root.MediaType, Path: path, Message: "Folder entry could not be read"})
			return nil
		}
		if d.IsDir() {
			m.update(jobID, func(p *Progress) {
				p.CurrentMediaType = root.MediaType
				p.CurrentPath = path
				p.DirectoriesScanned++
			})
			return nil
		}
		m.update(jobID, func(p *Progress) {
			p.CurrentMediaType = root.MediaType
			p.CurrentPath = path
			p.FilesDiscovered++
		})
		items = append(items, m.discoverFile(cleanRoot, root, path))
		return nil
	})
	if err != nil {
		return items, append(warnings, Warning{MediaType: root.MediaType, Path: cleanRoot, Message: err.Error()})
	}
	return items, warnings
}

func (m *Manager) discoverFile(root string, cfg rootConfig, path string) discoveredItem {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return unreadableCandidate(cfg.MediaType, root, path, "File is outside configured root")
	}
	ext := strings.ToLower(filepath.Ext(path))
	format := strings.TrimPrefix(ext, ".")
	info, statErr := os.Stat(path)
	filename := filepath.Base(path)
	base := Candidate{
		MediaType:      cfg.MediaType,
		Format:         format,
		Path:           path,
		RelativePath:   rel,
		Filename:       filename,
		Classification: ClassificationNew,
	}
	if statErr == nil {
		base.Size = info.Size()
		base.ModifiedAt = info.ModTime()
	}
	base.ID = candidateID(base)

	if _, ok := cfg.Formats[ext]; !ok {
		base.Classification = ClassificationUnsupported
		base.ClassificationReason = "Unsupported file extension for configured media root"
		return discoveredItem{State: ClassificationUnsupported, Candidate: base}
	}
	if statErr != nil {
		base.Classification = ClassificationUnreadable
		base.ClassificationReason = "File could not be read"
		base.Error = statErr.Error()
		return discoveredItem{State: ClassificationUnreadable, Candidate: base}
	}
	hash, err := fileSHA256(path)
	if err != nil {
		base.Classification = ClassificationUnreadable
		base.ClassificationReason = "File could not be read"
		base.Error = err.Error()
		return discoveredItem{State: ClassificationUnreadable, Candidate: base}
	}
	importCandidate := libraryimport.ImportCandidate{
		Path:         path,
		RelativePath: rel,
		OriginalPath: path,
		MediaType:    cfg.MediaType,
		Format:       format,
		Size:         info.Size(),
		ContentHash:  hash,
		Evidence: []libraryimport.PlanningEvidence{{
			Signal:      "library_scan_file",
			Value:       rel,
			Source:      "filesystem",
			Confidence:  library.ConfidenceExact,
			Explanation: "Library scanner discovered a supported file",
		}},
	}
	return discoveredItem{State: ClassificationNew, Candidate: base, ImportCandidate: importCandidate}
}

func unreadableCandidate(mediaType library.MediaType, root, path, msg string) discoveredItem {
	rel, _ := filepath.Rel(root, path)
	c := Candidate{
		ID:                   candidateID(Candidate{MediaType: mediaType, Path: path, RelativePath: rel}),
		MediaType:            mediaType,
		Format:               strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		Path:                 path,
		RelativePath:         rel,
		Filename:             filepath.Base(path),
		Classification:       ClassificationUnreadable,
		ClassificationReason: msg,
		Error:                msg,
	}
	return discoveredItem{State: ClassificationUnreadable, Candidate: c}
}

func applyPlan(candidate *Candidate, plan libraryimport.ImportPlan) {
	meta := metadataFromPlan(plan.Candidate)
	candidate.Metadata = meta
	candidate.Title = meta.Title
	candidate.Author = meta.Author
	candidate.Classification = ClassificationNew
	candidate.ClassificationReason = "Ready to import"

	switch plan.Disposition {
	case libraryimport.DispositionIgnoreDuplicate:
		if plan.File.Existing != nil {
			candidate.ExistingPath = plan.File.Existing.Path
		}
		reason := duplicateReason(plan)
		if reason == "path" {
			candidate.Classification = ClassificationAlreadyImported
			candidate.ClassificationReason = "Existing library file already uses this path"
		} else {
			candidate.Classification = ClassificationDuplicate
			candidate.ClassificationReason = "Existing library file already has matching content"
		}
	case libraryimport.DispositionConflict:
		if plan.File.Existing != nil {
			candidate.ExistingPath = plan.File.Existing.Path
		}
		candidate.Classification = ClassificationDuplicate
		candidate.ClassificationReason = firstEvidenceExplanation(plan, "Import planner found a duplicate or conflicting file")
	case libraryimport.DispositionNeedsManualReview:
		candidate.Classification = ClassificationNew
		candidate.ClassificationReason = firstEvidenceExplanation(plan, "Needs manual review before import")
	default:
		candidate.Classification = ClassificationNew
		candidate.ClassificationReason = "Ready to import"
	}
}

func metadataFromPlan(candidate libraryimport.ImportCandidate) Metadata {
	meta := Metadata{
		Title:      strings.TrimSpace(candidate.Metadata.SelectedTitle),
		Author:     strings.TrimSpace(candidate.Metadata.SelectedAuthor),
		Source:     "filename_fallback",
		Confidence: library.ConfidenceMedium,
	}
	if candidate.Metadata.EmbeddedTitle != "" || candidate.Metadata.EmbeddedAuthor != "" {
		meta.Source = "embedded_metadata"
		meta.Confidence = library.ConfidenceHigh
	}
	if meta.Title == "" && candidate.Metadata.FilenameTitle != "" {
		meta.Title = candidate.Metadata.FilenameTitle
	}
	if meta.Author == "" && candidate.Metadata.FilenameAuthor != "" {
		meta.Author = candidate.Metadata.FilenameAuthor
	}
	if meta.Title == "" {
		meta.Title = strings.TrimSuffix(filepath.Base(candidate.Path), filepath.Ext(candidate.Path))
		meta.Source = "filename_fallback"
		meta.Confidence = library.ConfidenceLow
	}
	return meta
}

func duplicateReason(plan libraryimport.ImportPlan) string {
	for _, evidence := range plan.File.Evidence {
		switch evidence.Signal {
		case "duplicate_path":
			return "path"
		case "duplicate_content_hash":
			return "content_hash"
		}
	}
	return ""
}

func firstEvidenceExplanation(plan libraryimport.ImportPlan, fallback string) string {
	for _, evidence := range plan.File.Evidence {
		if strings.TrimSpace(evidence.Explanation) != "" {
			return evidence.Explanation
		}
	}
	for _, evidence := range plan.Evidence {
		if strings.TrimSpace(evidence.Explanation) != "" {
			return evidence.Explanation
		}
	}
	return fallback
}

func countTotals(candidates []Candidate) Totals {
	totals := Totals{Found: len(candidates)}
	for _, c := range candidates {
		switch c.Classification {
		case ClassificationNew:
			totals.ReadyToImport++
		case ClassificationAlreadyImported:
			totals.AlreadyImported++
		case ClassificationDuplicate:
			totals.Duplicates++
		case ClassificationUnsupported:
			totals.Unsupported++
		case ClassificationUnreadable:
			totals.Unreadable++
		}
	}
	return totals
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func candidateID(c Candidate) string {
	h := sha256.New()
	_, _ = io.WriteString(h, string(c.MediaType))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, c.Path)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, c.RelativePath)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, fmt.Sprint(c.Size))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, fmt.Sprint(c.ModifiedAt.UnixNano()))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func newJobID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b[:])
}

func (m *Manager) update(jobID string, fn func(*Progress)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[jobID]; job != nil {
		fn(&job.Progress)
		job.Status = job.Progress.Status
	}
}

func (m *Manager) jobsStartedAt(jobID string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[jobID]; job != nil {
		return job.StartedAt
	}
	return time.Time{}
}

func (m *Manager) pruneLocked() {
	if len(m.order) <= maxCompletedJobs {
		return
	}
	kept := m.order[:0]
	for _, id := range m.order {
		job := m.jobs[id]
		if id == m.activeID || job == nil || job.CompletedAt == nil {
			kept = append(kept, id)
			continue
		}
		if len(m.order)-len(kept) <= maxCompletedJobs {
			kept = append(kept, id)
			continue
		}
		delete(m.jobs, id)
	}
	m.order = kept
}

func (m *Manager) cloneJobLocked(jobID string) *Job {
	return cloneJob(m.jobs[jobID])
}

func cloneJob(job *Job) *Job {
	if job == nil {
		return nil
	}
	copy := *job
	copy.Progress.Warnings = append([]Warning(nil), job.Progress.Warnings...)
	if job.Result != nil {
		result := *job.Result
		result.Candidates = append([]Candidate(nil), job.Result.Candidates...)
		result.Warnings = append([]Warning(nil), job.Result.Warnings...)
		copy.Result = &result
	}
	return &copy
}

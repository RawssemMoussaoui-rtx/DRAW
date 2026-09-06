// Package ingestion implements Phase E parsing/dispatch for retrieved content.
//
// Modules A/B/C own content-type dispatch, URL resolution, and content
// fingerprinting respectively. This file contains the Processor, which composes
// them: it consumes a model.TaskResult, deduplicates the body via the
// Fingerprinter, extracts & resolves URLs, and pushes URLCandidates into a
// frontier.FrontierSink.
//
// The Processor implements the master.Ingestion seam structurally; it holds
// ONLY the FrontierSink capability and never calls Frontier.Next. Ingestion is
// best-effort and non-fatal: every error path returns nil.
package ingestion

import (
	"context"
	"strings"

	"draw/internal/frontier"
	"draw/internal/model"
)

// defaultMaxURLsPerPage is the cap on discovered URLs pushed per processed
// result when WithMaxURLsPerPage is unset or <= 0.
const defaultMaxURLsPerPage = 2000

// Processor owns frontier.FrontierSink dispatch for retrieved content. It
// composes the extraction, normalization, and fingerprinting modules (A/B/C)
// into a single best-effort result observer.
type Processor struct {
	sink    frontier.FrontierSink
	fp      *Fingerprinter
	maxURLs int
}

// ProcessorOpt configures a Processor.
type ProcessorOpt func(*Processor)

// WithMaxURLsPerPage sets the cap on discovered URLs pushed per processed
// result. A value <= 0 selects the default cap (2000).
func WithMaxURLsPerPage(n int) ProcessorOpt {
	return func(p *Processor) { p.maxURLs = n }
}

// NewProcessor creates a Processor that pushes discovered URLs into sink.
// Each Processor owns its own Fingerprinter session so that duplicate bodies
// discovered multiple times within one Processor's lifetime are processed only
// once.
func NewProcessor(sink frontier.FrontierSink, opts ...ProcessorOpt) *Processor {
	if sink == nil {
		panic("ingestion: Processor requires a non-nil FrontierSink")
	}
	p := &Processor{
		sink:    sink,
		fp:      NewFingerprinter(),
		maxURLs: defaultMaxURLsPerPage,
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.maxURLs <= 0 {
		p.maxURLs = defaultMaxURLsPerPage
	}
	return p
}

// Process implements the master.Ingestion seam (structurally). It is
// best-effort and non-fatal: it never returns a non-nil error. A non-nil
// context is honored for cancellation, but no downstream call here blocks
// meaningfully; the ctx is surfaced for parity with the seam signature.
func (p *Processor) Process(ctx context.Context, t model.Task, r model.TaskResult) error {
	_ = ctx

	// Only content-bearing retrieval results have crawlable bodies.
	switch r.Status {
	case model.RetrievalStatusSuccess, model.RetrievalStatusPartial:
	default:
		return nil
	}

	if len(r.Data) == 0 {
		return nil
	}

	// Skip redundant extraction for bodies identical to one already processed
	// this session. Prevents redundant discovery loops.
	if p.fp.Seen(r.Data) {
		return nil
	}

	ctype := strings.ToLower(r.Headers.Get("Content-Type"))
	baseHref, links, _ := ExtractURLs(ctype, r.Data)
	if len(links) == 0 {
		return nil
	}

	resolved := Resolve(t.URL, baseHref, links)
	if len(resolved) == 0 {
		return nil
	}

	if p.maxURLs > 0 && len(resolved) > p.maxURLs {
		resolved = resolved[:p.maxURLs]
	}

	// One canonical URLID per processed page; all candidates from this page
	// share it so the frontier can attribute them to the same source.
	pageURLID := model.NewURLID()
	parent := t.ID

	candidates := make([]frontier.URLCandidate, 0, len(resolved))
	for _, u := range resolved {
		candidates = append(candidates, frontier.URLCandidate{
			SessionID:         t.SessionID,
			URL:               u,
			Domain:            strings.ToLower(u.Hostname()),
			SourceClass:       t.SourceClass,
			ParentTaskID:      &parent,
			DiscoveredFromURL: &pageURLID,
			CrawlDepth:        t.CrawlDepth + 1,
			TaskDepth:         t.TaskDepth + 1,
			PriorityHint:      t.Priority,
		})
	}

	_ = p.sink.Push(candidates)
	return nil
}

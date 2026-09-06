package frontier

import (
	"net/url"

	"draw/internal/model"
)

type FrontierSink interface {
	Push(urls []URLCandidate) error
}

type Frontier interface {
	FrontierSink
	Next(n int) []URLCandidate
	Has(domain, rawurl string) bool
	Score(c URLCandidate) float64
}

type URLCandidate struct {
	SessionID         model.SessionID
	URL               *url.URL
	Domain            string
	SourceClass       model.SourceClass
	ParentTaskID      *model.TaskID
	DiscoveredFromURL *model.URLID
	CrawlDepth        int
	TaskDepth         int
	PriorityHint      int
}

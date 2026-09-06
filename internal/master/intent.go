package master

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"draw/internal/model"
)

type IntentEngine interface {
	Parse(model.IntentRequest) (model.Intent, error)
}

type IntentConfig struct {
	DefaultCrawlDepth   int
	DefaultTaskDepth    int
	DefaultEffortBudget int
	DefaultMaxReplans   int
}

func DefaultIntentConfig() IntentConfig {
	return IntentConfig{
		DefaultCrawlDepth:   2,
		DefaultTaskDepth:    3,
		DefaultEffortBudget: 500,
		DefaultMaxReplans:   3,
	}
}

type IntentParser struct {
	Defaults IntentConfig
}

func NewIntentParser(cfg IntentConfig) IntentParser {
	def := DefaultIntentConfig()
	if cfg.DefaultCrawlDepth <= 0 {
		cfg.DefaultCrawlDepth = def.DefaultCrawlDepth
	}
	if cfg.DefaultTaskDepth <= 0 {
		cfg.DefaultTaskDepth = def.DefaultTaskDepth
	}
	if cfg.DefaultEffortBudget <= 0 {
		cfg.DefaultEffortBudget = def.DefaultEffortBudget
	}
	if cfg.DefaultMaxReplans <= 0 {
		cfg.DefaultMaxReplans = def.DefaultMaxReplans
	}
	return IntentParser{Defaults: cfg}
}

var (
	timeWindowRe  = regexp.MustCompile(`(?i)\b(last|past|within|in)\s+(\d+)\s*(day|hour|week|month|year)s?\b`)
	researchRe    = regexp.MustCompile(`(?i)^research\s+`)
	sourceTokenRe = regexp.MustCompile(`(?i)\b(official|news|social|specialized)\b`)
)

var entityEndMarkers = []string{
	",", "last", "past", "compare", "vs", "versus", "sources", "source",
	"for", "about", "on", "within", "in", "next", "this",
}

func (p IntentParser) Parse(req model.IntentRequest) (model.Intent, error) {
	if req.Query == "" && len(req.Seeds) == 0 {
		return model.Intent{}, errEmptyRequest
	}

	intent := model.Intent{
		ID:           model.NewIntentID(),
		Type:         model.IntentTypeResearch,
		OutputType:   model.OutputTypeReport,
		CrawlDepth:   p.Defaults.DefaultCrawlDepth,
		TaskDepth:    p.Defaults.DefaultTaskDepth,
		EffortBudget: p.Defaults.DefaultEffortBudget,
		MaxReplans:   p.Defaults.DefaultMaxReplans,
		Seeds:        append([]string(nil), req.Seeds...),
	}

	body := stripLeadingResearch(req.Query)
	intent.Entity = extractEntity(body)
	intent.SourceClasses = extractSourceClasses(body)
	intent.TimeRange = extractTimeWindow(body)
	intent.Operations = deriveOperations(hasCompare(body), intent.SourceClasses)
	intent.Topics = extractTopics(body, intent.Entity)

	if intent.Entity == "" && len(intent.Seeds) > 0 {
		intent.Entity = seedDomain(intent.Seeds[0])
	}

	return intent, nil
}

var errEmptyRequest = &parseError{msg: "intent request must provide a query or at least one seed"}

type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }

func stripLeadingResearch(q string) string {
	s := strings.TrimSpace(q)
	if researchRe.MatchString(s) {
		return strings.TrimSpace(s[len("research"):])
	}
	return s
}

func hasCompare(q string) bool {
	l := strings.ToLower(q)
	return strings.Contains(l, "compare") || strings.Contains(l, " vs ") || strings.Contains(l, "versus")
}

func extractEntity(q string) string {
	s := strings.TrimSpace(q)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	earliest := len(s)
	marker := ""
	for _, m := range entityEndMarkers {
		if idx := strings.Index(low, m); idx >= 0 && idx < earliest {
			earliest = idx
			marker = m
		}
	}
	if marker == "" {
		return s
	}
	return strings.TrimSpace(s[:earliest])
}

func extractSourceClasses(q string) []model.SourceClass {
	seen := map[model.SourceClass]bool{}
	out := []model.SourceClass{}
	for _, m := range sourceTokenRe.FindAllString(q, -1) {
		var c model.SourceClass
		switch strings.ToLower(m) {
		case "official":
			c = model.SourceClassOfficial
		case "news":
			c = model.SourceClassNews
		case "social":
			c = model.SourceClassSocial
		case "specialized":
			c = model.SourceClassSpecialized
		default:
			continue
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func extractTimeWindow(q string) *model.TimeWindow {
	m := timeWindowRe.FindStringSubmatch(q)
	if m == nil {
		return nil
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return nil
	}
	var unit time.Duration
	switch m[3] {
	case "hour":
		unit = time.Hour
	case "day":
		unit = 24 * time.Hour
	case "week":
		unit = 7 * 24 * time.Hour
	case "month":
		unit = 30 * 24 * time.Hour
	case "year":
		unit = 365 * 24 * time.Hour
	default:
		return nil
	}
	to := time.Now().UTC().Truncate(time.Second)
	return &model.TimeWindow{From: to.Add(-time.Duration(n) * unit), To: to}
}

func deriveOperations(compare bool, _ []model.SourceClass) []model.Operation {
	ops := []model.Operation{
		model.OperationDiscover,
		model.OperationRetrieve,
		model.OperationVerify,
		model.OperationContradictionCheck,
	}
	if compare {
		ops = append(ops, model.OperationCompare)
	}
	return ops
}

func extractTopics(q, entity string) []string {
	rest := strings.TrimSpace(q)
	if entity != "" {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, entity))
	}
	rest = trimLeadingMarkers(rest)
	if rest == "" {
		return nil
	}
	parts := strings.Split(rest, ",")
	topics := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if hasMarkerPrefix(p) {
			break
		}
		topics = append(topics, p)
	}
	return topics
}

func trimLeadingMarkers(s string) string {
	low := strings.ToLower(s)
	for _, m := range entityEndMarkers {
		if strings.HasPrefix(low, m) {
			return strings.TrimSpace(s[len(m):])
		}
	}
	return s
}

func hasMarkerPrefix(s string) bool {
	low := strings.ToLower(s)
	for _, m := range entityEndMarkers {
		if strings.HasPrefix(low, m) {
			return true
		}
	}
	return false
}

func seedDomain(seed string) string {
	s := strings.TrimSpace(seed)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return u.Host
	}
	return s
}

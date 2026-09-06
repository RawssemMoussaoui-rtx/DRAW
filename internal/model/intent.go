package model

type IntentRequest struct {
	Query  string
	Seeds  []string
	UserID string
}

type Intent struct {
	ID            IntentID
	Type          IntentType
	Entity        string
	Topics        []string
	TimeRange     *TimeWindow
	SourceClasses []SourceClass
	Operations    []Operation
	OutputType    OutputType
	CrawlDepth    int
	TaskDepth     int
	EffortBudget  int
	MaxReplans    int
	Seeds         []string
}

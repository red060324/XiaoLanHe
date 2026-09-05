package entity

type Message struct {
	ID            int64
	Role, Content string
}

type SummaryCandidate struct {
	PriorSummary                     string
	PriorWatermark, ThroughMessageID int64
	Messages                         []Message
}

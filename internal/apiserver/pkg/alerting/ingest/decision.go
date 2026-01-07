package ingest

const (
	DecisionNormal   = "normal"
	DecisionMerged   = "merged"
	DecisionSilenced = "silenced"
	DecisionDeduped  = "deduped"
)

// Decision captures policy judgment for one alert ingest request.
type Decision struct {
	Decision         string
	Backend          string
	Silenced         bool
	SilenceID        string
	Deduped          bool
	BurstSuppressed  bool
	SuppressIncident bool
	SuppressTimeline bool
}

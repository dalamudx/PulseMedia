package ports

import "context"

type QueueMetrics struct {
	Queue, State         string
	Count, ExpiredLeases int64
	ReadyAgeSeconds      float64
}
type RuntimeMetrics struct {
	PoolTotal, PoolIdle, PoolAcquired, PoolMax             int32
	AcquireCount, EmptyAcquireCount, CancelledAcquireCount int64
	AcquireSeconds, EmptyAcquireSeconds                    float64
	LockWaiters                                            int64
	LockWaitAgeSeconds                                     float64
	Queues                                                 []QueueMetrics
	IndexerBudget                                          IndexerBudgetMetrics
	Stages                                                 [WorkStageCount]WorkStageMetrics
}
type RuntimeMetricsReader interface {
	ReadRuntimeMetrics(context.Context) (RuntimeMetrics, error)
}

// Fixed dimensions keep observations independent of payloads and object identities.
type WorkStage uint8

const (
	StageDownloadSubmission WorkStage = iota
	StageDownloadReconcile
	StageDownloadObserve
	StageLibraryScan
	StageDownloadOrganization
	StageLibraryReuse
	StagePersonFollowSchedule
	StageDoubanWishSchedule
	StageCookieCloudSchedule
	WorkStageCount
)

func WorkStageLabels() [WorkStageCount]string {
	return [WorkStageCount]string{"download_submission", "download_reconcile", "download_observe", "library_scan", "download_organization", "library_reuse", "person_follow_schedule", "douban_wish_schedule", "cookiecloud_schedule"}
}
func WorkStageDurationBounds() [7]float64 { return [7]float64{.1, 1, 5, 30, 120, 600, 3600} }

type WorkStageMetrics struct {
	InFlight int64
	Returned uint64
	Failures uint64
	Seconds  float64
	Buckets  [7]uint64
}
type IndexerBudgetMetrics struct {
	Open, Interval, Cooldown int64
	MaxDelaySeconds          float64
}

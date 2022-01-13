package ratelimit

import (
	"sync"
	"time"

	"github.com/elastic/beats/v7/libbeat/common/atomic"
	"github.com/elastic/beats/v7/libbeat/logp"
	"github.com/elastic/go-concert/unison"
	"github.com/jonboulle/clockwork"
)

func init() {
	register("size_controller", newSizeController)
}

type sizeBucket struct {
	totalSize     uint64
	lastReplenish time.Time
}

type sizeController struct {
	mu unison.Mutex

	limit   rate
	depth   float64
	buckets sync.Map

	// GC thresholds and metrics
	gc struct {
		thresholds tokenBucketGCConfig
		metrics    struct {
			numCalls atomic.Uint
		}
	}

	clock  clockwork.Clock
	logger *logp.Logger
}

func newSizeController(config algoConfig) (algorithm, error) {
	return &sizeController{}, nil
}

func (c *sizeController) IsAllowed(uint64) bool {
	return true
}

// setClock allows test code to inject a fake clock
func (c *sizeController) setClock(k clockwork.Clock) {
	c.clock = k
}

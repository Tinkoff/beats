// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package sizeratelimit

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/mitchellh/hashstructure"
	"github.com/pkg/errors"

	"github.com/elastic/beats/v7/libbeat/beat"
	"github.com/elastic/beats/v7/libbeat/common"
	"github.com/elastic/beats/v7/libbeat/common/atomic"
	"github.com/elastic/beats/v7/libbeat/logp"
	"github.com/elastic/beats/v7/libbeat/monitoring"
	"github.com/elastic/beats/v7/libbeat/processors"
)

// instanceID is used to assign each instance a unique monitoring namespace.
var instanceID = atomic.MakeUint32(0)

const processorName = "size_rate_limit"
const logName = "processor." + processorName

func init() {
	processors.RegisterPlugin(processorName, new)
}

type bucket struct {
	Bytes         uint64
	LastReplenish time.Time
}

type metrics struct {
	Dropped *monitoring.Int
}

type sizeRateLimit struct {
	config  config
	logger  *logp.Logger
	metrics metrics
	keys    map[uint64]bucket
	clock   clockwork.Clock
}

// new constructs a new rate limit processor.
func new(cfg *common.Config) (processors.Processor, error) {
	var config config
	if err := cfg.Unpack(&config); err != nil {
		return nil, errors.Wrap(err, "could not unpack processor configuration")
	}

	// Logging and metrics (each processor instance has a unique ID).
	var (
		id  = int(instanceID.Inc())
		log = logp.NewLogger(logName).With("instance_id", id)
		reg = monitoring.Default.NewRegistry(logName+"."+strconv.Itoa(id), monitoring.DoNotReport)
	)

	sort.Strings(config.Fields)

	p := &sizeRateLimit{
		config: config,
		logger: log,
		metrics: metrics{
			Dropped: monitoring.NewInt(reg, "dropped"),
		},
		clock: clockwork.NewRealClock(),
		keys:  map[uint64]bucket{},
	}

	return p, nil
}

// Run applies the configured rate limit to the given event. If the event is within the
// configured rate limit, it is returned as-is. If not, nil is returned.
func (p *sizeRateLimit) Run(event *beat.Event) (*beat.Event, error) {
	messageLen, ok := event.Meta["message_len"]
	if !ok {
		return nil, fmt.Errorf("message_len meta not found in event")
	}
	key, err := p.makeKey(event)
	if err != nil {
		return nil, errors.Wrap(err, "could not make key")
	}
	if len(p.keys) > 10 {
		p.clearOldKeys()
	}

	if p.checkLimit(key, messageLen.(uint64)) {
		return event, nil
	}

	p.logger.Debugf("event [%v] dropped by size_rate_limit processor", event)
	p.metrics.Dropped.Inc()
	return nil, nil
}

func (p *sizeRateLimit) String() string {
	return fmt.Sprintf(
		"%v=[limit=[%v],fields=[%v]]",
		processorName, p.config.Limit, p.config.Fields,
	)
}

func (p *sizeRateLimit) makeKey(event *beat.Event) (uint64, error) {
	if len(p.config.Fields) == 0 {
		return 0, nil
	}

	values := make([]string, len(p.config.Fields))
	for _, field := range p.config.Fields {
		value, err := event.GetValue(field)
		if err != nil {
			if err != common.ErrKeyNotFound {
				return 0, errors.Wrapf(err, "error getting value of field: %v", field)
			}

			value = ""
		}

		values = append(values, fmt.Sprintf("%v", value))
	}

	return hashstructure.Hash(values, nil)
}

func (p *sizeRateLimit) checkLimit(key uint64, messageLen uint64) bool {
	_, ok := p.keys[key]
	if !ok {
		p.keys[key] = bucket{0, time.Now()}
	}
	secsSinceLast := p.clock.Now().Sub(p.keys[key].LastReplenish).Seconds()
	p.keys[key] = bucket{p.keys[key].Bytes + messageLen, p.clock.Now()}
	currentRate := float64(p.keys[key].Bytes) / secsSinceLast
	return currentRate > p.config.Limit.valuePerSecond()
}

func (p *sizeRateLimit) clearOldKeys() {
	for key := range p.keys {
		secsSinceLast := p.clock.Now().Sub(p.keys[key].LastReplenish).Seconds()
		if secsSinceLast > 10 {
			delete(p.keys, key)
		}
	}
}

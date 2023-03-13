package worker

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/exp/slices"
	"strconv"
	"time"

	ec "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/namespace"
	"go.uber.org/zap"
)

var (
	// ErrClockDriftTryLater and ErrClockDriftTooLargeToRecover both indicate the client to retry and hopefully the retry will land on a different worker.
	ErrClockDriftTryLater          = errors.New("clock drift. try later")
	ErrClockDriftTooLargeToRecover = errors.New("clock drift too large to cover")

	// ErrNotReady indicates the worker is not in ready state: either in starting state or stopped state.
	ErrNotReady = errors.New("worker is not ready to serve request")

	maxClockDriftToBlockInMillis       = int64(100)
	maxClockDriftToReturnErrorInMillis = int64(5000)
)

const (
	// The prefix of key representing whether the worker is live, e.g., "workers/live/10".
	// The key is associated with a lease. The value is the timestamp the worker claimed th worker ID.
	liveWorkerKeyPrefix = "workers/live"

	// The prefix of key representing the last reported milliseconds of the worker, e.g., "workers/last_reported_time"
	// The value is timestamp last reported by a worker.
	lastReportedTimeKeyPrefix = "workers/last_reported_time"

	// heartbeat must be more frequent than lease refresh.
	leaseTTL              = 10 // 10 seconds
	heartbeatReportPeriod = 3 * time.Second

	workerStartTimeout = time.Minute

	// max time allowed to run etcd command
	etcdCommandTimeout = time.Second

	etcdDialTimeout = time.Second

	stateStarting = 1
	stateReady    = 2
	stateStopped  = 3
)

// Worker coordinates through etcd to generate snowflake IDs.
//  1. Coordinate through etcd to join and leave a cluster of workers.
//     1.1. At any point in time, no two workers have the same worker ID.
//     1.2. If a worker leaves the cluster and the worker ID is reused later, the timestamp associated with the worker ID
//     must be monotonic increasing. Therefore, workers periodically uploads its timestamp to etcd.
//
// 2. Generate snowflake ID.
type Worker struct {
	etcdClient *ec.Client

	generator    *Generator
	workerID     int
	leaseID      ec.LeaseID
	maxWorkerCnt int

	heartbeatTicker *time.Ticker

	// the state of the worker transition: starting -> ready -> stopped.
	state int

	// notification channel to stop and exit
	stopChan chan struct{}
}

// NewWorker creates a new worker and adds the worker to a cluster.
func NewWorker(endpoints []string, prefix, username, password string, maxWorkerCnt int) (*Worker, error) {
	cli, err := ec.New(ec.Config{
		Endpoints:   endpoints,
		DialTimeout: etcdDialTimeout,
		Username:    username,
		Password:    password,
	})
	if err != nil {
		return nil, err
	}

	cli.KV = namespace.NewKV(cli.KV, prefix)
	cli.Watcher = namespace.NewWatcher(cli.Watcher, prefix)
	cli.Lease = namespace.NewLease(cli.Lease, prefix)

	// Make sure the connection is good and we have the permission to operate under the prefix.
	ctx, cancel := context.WithTimeout(context.Background(), etcdCommandTimeout)
	defer cancel()
	_, err = cli.Get(ctx, "hello")
	if err != nil {
		return nil, err
	}

	s := Worker{
		etcdClient:   cli,
		maxWorkerCnt: maxWorkerCnt,
		state:        stateStarting,
		stopChan:     make(chan struct{}, 1),
	}

	if !s.start() {
		return nil, fmt.Errorf("cannot start snowflake id worker")
	}
	s.state = stateReady

	return &s, nil
}

// NextID returns a snowflake ID.
// To avoid duplicate snowflake IDs, the timestamp in milliseconds must be monotonically increasing.
// The timestamp used here is the wall-clock time. If timestamp goes backward, we call it a wall-clock drift.
// In Go, if two consecutive time.Now() returns t1 and t2 respectively, the following statements are true.
// 1. t1 is always before t2. That means, t2.Before(t1) is true, and t2.Sub(t1) is positive. This is because time.Time
// is monotonic since 1.9. See [issue-12914](https://go.googlesource.com/proposal/+/master/design/12914-monotonic.md).
// 2. t2.UnixMilli() is larger than t1.UnixMilli() in most cases. But in cases of wall clock going backwards,
// t2.UnixMilli() < t1.UnixMilli(). For example, positive leap seconds, NTP adjustment,
// or the worker's clock is reset to a previous datetime.
//
// We categorize the clock-drift in 3 buckets and handle them accordingly.
// bucket 1: [0, maxClockDriftToBlockInMillis): block by the drift time and return success.
// bucket 2: [maxClockDriftToBlockInMillis, maxClockDriftToReturnErrorInMillis]: return error (try later) immediately. We expect clients retry request on a different worker.
// bucket 3: [maxClockDriftToReturnErrorInMillis, infinity): something is wrong, we return error (try later) and remove the worker from the cluster.
// We recommend setting maxClockDriftToBlockInMillis to some value between 1 second and 2 seconds. That should avoid where all workers have a short clock drift (e.g. leap second)
// and client retries double the requests.
func (s *Worker) NextID() (int64, error) {
	if !s.Ready() {
		return 0, ErrNotReady
	}

	id, drift := s.generator.Next()
	switch {
	case drift <= 0:
		return id, nil
	case drift > 0 && drift < maxClockDriftToBlockInMillis:
		zap.S().Errorf("clock drift, block till clock catch up. drift %d", drift)
		time.Sleep(time.Duration(drift) * time.Millisecond)
		id, drift = s.generator.Next()
		if drift == 0 {
			return id, nil
		}
		return 0, ErrClockDriftTryLater
	case drift < maxClockDriftToReturnErrorInMillis:
		zap.S().Errorf("clock drift, do not block, return error so client can try later on a different worker. drift: %d", drift)
		return 0, ErrClockDriftTryLater
	default:
		zap.S().Errorf("clock drift too large. stop the worker. drift: %d", drift)
		s.Stop()
		return 0, ErrClockDriftTooLargeToRecover
	}
	return id, nil
}

// start starts the worker by joining the cluster and setup background goroutines.
func (s *Worker) start() bool {
	ctx, cancel := context.WithTimeout(context.Background(), workerStartTimeout)
	defer cancel()

	resp, err := s.etcdClient.Grant(ctx, leaseTTL)
	if err != nil {
		zap.S().Error("cannot grant lease", err)
		return false
	}

	s.leaseID = resp.ID
	alive, err := s.etcdClient.KeepAlive(context.Background(), s.leaseID)
	if err != nil {
		zap.S().Errorf("cannot refresh lease %v", err)
		return false
	}
	// Start a goroutine to consume lease keepalive responses. No need to handle the response for correctness.
	// Consume response here to avoid warning logs of "channel full" from the etcd client code.
	go func() {
		for range alive {
		}
	}()

	unavailableIDs := s.workerIDsInUse(ctx)
	id := 0
	var g *Generator
	for ; id < s.maxWorkerCnt; id++ {
		if slices.Contains(unavailableIDs, id) {
			continue
		}
		if !s.isWorkerIDAvailable(ctx, id) {
			continue
		}
		if g, err = NewGenerator(id); err != nil {
			zap.S().Infof("cannot create generator with worker id %d", id)
			continue
		}
		s.workerID = id
		s.generator = g
		if !s.claimWorkerID(ctx, id) {
			zap.S().Infof("cannot claim worker id %d", id)
			continue
		} else {
			break
		}
	}
	if id == s.maxWorkerCnt {
		zap.S().Errorf("exhausted %d worker IDs", s.maxWorkerCnt)
		return false
	}

	zap.S().Infof("claimed worker id %d", id)
	s.heartbeatTicker = time.NewTicker(heartbeatReportPeriod)

	// Start go routine to report lsat used timestamp periodically.
	go func() {
		for range s.heartbeatTicker.C {
			if !s.reportLastUsedTime() {
				s.Stop()
				return
			}
		}
	}()
	return true
}

func (s *Worker) reportLastUsedTime() bool {
	workerKey := liveWorkerKey(s.workerID)
	reportedTimeKey := lastReportedTimestampKey(s.workerID)
	reportedTime := strconv.FormatInt(s.generator.LastUsedTimestamp(), 10)

	ctx, cancel := context.WithTimeout(context.Background(), etcdCommandTimeout)
	defer cancel()

	commit, err := s.etcdClient.Txn(ctx).If(
		ec.Compare(ec.LeaseValue(workerKey), "=", s.leaseID),
	).Then(
		ec.OpPut(reportedTimeKey, reportedTime),
	).Else(
		ec.OpGet(workerKey),
	).Commit()

	if err != nil || !commit.Succeeded || len(commit.Responses) != 1 {
		zap.S().Errorf("cannot report last used timestamp, %v, %+v", err, commit)
		return false
	}

	op := commit.Responses[0]
	if op.GetResponseRange() != nil {
		zap.S().Errorf("lease id of key %d is differernt from worker's lease id %d",
			op.GetResponseRange().Kvs[0].Lease, s.leaseID)
		return false
	}
	if op.GetResponsePut() == nil {
		zap.S().Errorf("Unrecognized transaction response. %+v. It should either be a Get or Put.", op)
		return false
	}
	zap.S().Infof("reported last used timestamp %s", reportedTime)
	return true
}

// Stop stops the worker. This is public method so that it can be called, e.g. called by the main thread on app exit.
func (s *Worker) Stop() {
	if s.state == stateStopped {
		return
	}

	s.state = stateStopped
	ctx, cancel := context.WithTimeout(context.Background(), etcdCommandTimeout)
	defer cancel()

	_, err := s.etcdClient.Revoke(ctx, s.leaseID)
	if err != nil {
		zap.S().Errorf("cannot revoke lease %d", s.leaseID)
	}
	if s.heartbeatTicker != nil {
		s.heartbeatTicker.Stop()
	}

	s.stopChan <- struct{}{}
}

// isWorkerIDAvailable checks whether the current worker can claim a worker ID.
func (s *Worker) isWorkerIDAvailable(ctx context.Context, id int) bool {
	resp, err := s.etcdClient.Get(ctx, liveWorkerKey(id))
	if err != nil {
		zap.S().Error(err)
		return false
	}
	if resp.Count > 0 {
		zap.S().Errorf("worker id %d is in use", id)
		return false
	}

	resp, err = s.etcdClient.Get(ctx, lastReportedTimestampKey(id))
	if err != nil {
		zap.S().Error(err)
		return false
	}
	// The worker-id has been used by some worker previously. But the worker has stopped using the ID for some reason.
	// If the current milliseconds are larger than (the last reported milliseconds of the stopped worker + heartbeatReportPeriod),
	// then the current worker can reuse the worker id.
	if resp.Count > 0 {
		t, err := strconv.ParseInt(string(resp.Kvs[0].Value), 10, 64)
		if err != nil {
			zap.S().Error(err)
			return false
		}
		if time.Now().UnixMilli() < t+heartbeatReportPeriod.Milliseconds() {
			return false
		}
	}
	return true
}

// claimWorkerID takes the worker id using etcd transactions.
// Because etcd transactions are serializable, no two workers can take the same ID.
func (s *Worker) claimWorkerID(ctx context.Context, id int) bool {
	workerKey := liveWorkerKey(id)
	reportedTimeKey := lastReportedTimestampKey(id)

	commit, err := s.etcdClient.Txn(ctx).If(
		// test whether the worker ID is in use.
		ec.Compare(ec.Version(workerKey), "=", 0),
	).Then(
		// claim the worker ID.
		ec.OpPut(workerKey, strconv.FormatInt(time.Now().UnixMilli(), 10), ec.WithLease(s.leaseID)),
		ec.OpGet(reportedTimeKey),
	).Else(
		// return the existing record of the worker ID.
		ec.OpGet(workerKey),
	).Commit()
	if err != nil || !commit.Succeeded {
		zap.S().Warnf("Cannot claim worker id %d in a transaction, %v, %+v", id, err, commit)
		return false
	}

	switch len(commit.Responses) {
	// executed the Else branch. The id is in use.
	case 1:
		zap.S().Warnf("Worker id %d already claimed by another worker with lease id %d", id,
			commit.Responses[0].GetResponseRange().Kvs[0].Lease)
		return false
	// executed the If branch. Claimed worker id and return the last reported time associated with the worker id.
	case 2:
		r := commit.Responses[1].GetResponseRange()
		if r.Count == 0 {
			break
		}
		// The worker has been used previously and there is a reported timestamp.
		v := string(r.Kvs[0].Value)
		t, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			zap.S().Error("Reported timestamp not recognized. %s", v)
			return false
		}
		s.generator.SetLastUsedTimestamp(t)
		zap.S().Infof("Set last reported timestamp to %d", t)
	default:
		zap.S().Errorf("Unrecognized claim worker id transaction response. %+v", commit.Responses)
		return false
	}
	zap.S().Infof("Successfully claimed worker id, %d", id)
	return true
}

func (s *Worker) workerIDsInUse(ctx context.Context) []int {
	resp, err := s.etcdClient.Get(ctx, liveWorkerKeyPrefix, ec.WithPrefix())
	if err != nil {
		zap.S().Error(err)
		return nil
	}
	var ids []int
	for _, r := range resp.Kvs {
		idStr := string(r.Key)[len(liveWorkerKeyPrefix)+1:]
		i, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			zap.S().Errorf("cannot parse worker id from %s, %v", string(r.Key), err)
			continue
		}
		ids = append(ids, int(i))
	}
	return ids
}

// StopChannel returns a channel that notifies when the worker has stopped.
func (s *Worker) StopChannel() <-chan struct{} {
	return s.stopChan
}

func (s *Worker) Ready() bool {
	return s.state == stateReady
}

func (s *Worker) Live() bool {
	return s.state != stateStopped
}

func liveWorkerKey(id int) string {
	return fmt.Sprintf("%s/%d", liveWorkerKeyPrefix, id)
}

func lastReportedTimestampKey(id int) string {
	return fmt.Sprintf("%s/%d", lastReportedTimeKeyPrefix, id)
}

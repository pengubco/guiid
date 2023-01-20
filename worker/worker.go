package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
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
	// The pattern of key representing whether the server is live, e.g., "servers/live/10".
	// The key is associated with a lease. The value is a metadata describing the server.
	liveServerKeyPattern = "servers/live/%d"

	// The pattern of key for the last reported milliseconds of the server.
	lastReportedTimeKeyPattern = "servers/last_reported_time/%d"

	// heartbeat must be more frequent than lease refresh.
	leaseTTL              = 10 // 10 seconds
	heartbeatReportPeriod = 3 * time.Second

	serverStartTimeout = 2 * time.Minute

	// max time allowed to run etcd command
	etcdCommandTimeout = 2 * time.Second

	etcdDialTimeout = 2 * time.Second

	stateStarting = 1
	stateReady    = 2
	stateStopped  = 3
)

// Worker coordinates through etcd to generate snowflake IDs. It does two tasks.
//  1. Coordinate through etcd to join and leave a cluster of workers.
//     1.1. At any point in time, no two workers have the same worker ID.
//     1.2. If a worker leaves the cluster and the worker ID is reused later, the timestamp associated with the worker ID
//     must be monotonic increasing. Therefore, workers periodically uploads its timestamp to etcd.
//
// 2. Generate snowflake ID.
type Worker struct {
	etcdClient *clientv3.Client

	generator    *Generator
	workerID     int
	leaseID      clientv3.LeaseID
	maxServerCnt int

	heartbeatTicker *time.Ticker

	// the state of the server transition: starting -> ready -> stopped.
	state int

	// notification channel to stop and exit
	stopChan chan struct{}
}

// NewWorker creates a new worker and adds the worker to a cluster.
func NewWorker(endpoints []string, prefix, username, password string, maxServerCnt int) (*Worker, error) {
	cli, err := clientv3.New(clientv3.Config{
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
		maxServerCnt: maxServerCnt,
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
// bucket 2: [maxClockDriftToBlockInMillis, maxClockDriftToReturnErrorInMillis]: return error (try later) immediately. We expect clients retry request on a different server.
// bucket 3: [maxClockDriftToReturnErrorInMillis, infinity): something is wrong, we return error (try later) and remove the worker from the cluster.
// We recommend setting maxClockDriftToBlockInMillis to some value between 1 second and 2 seconds. That should avoid where all servers have a short clock drift (e.g. leap second)
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
		zap.S().Errorf("clock drift too large. stop the server. drift: %d", drift)
		s.Stop()
		return 0, ErrClockDriftTooLargeToRecover
	}
	return id, nil
}

// start starts the worker by joining the cluster and setup background goroutines.
func (s *Worker) start() bool {
	ctx, cancel := context.WithTimeout(context.Background(), serverStartTimeout)
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

	// TODO: list available IDs, instead of going over ids 1 by 1.
	id := 0
	var g *Generator
	for ; id < s.maxServerCnt; id++ {
		if !s.isServerIDAvailable(ctx, id) {
			continue
		}
		if g, err = NewGenerator(id); err != nil {
			zap.S().Infof("cannot create generator with server id %d", id)
			continue
		}
		if !s.claimServerID(ctx, id) {
			zap.S().Infof("cannot claim server id %d", id)
			continue
		} else {
			break
		}
	}
	if id == s.maxServerCnt {
		zap.S().Errorf("exhausted %d server IDs", s.maxServerCnt)
		return false
	}

	zap.S().Infof("claimed server id %d", id)
	s.workerID = id
	s.generator = g
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
	liveServerKey := fmt.Sprintf(liveServerKeyPattern, s.workerID)
	reportedTimeKey := fmt.Sprintf(lastReportedTimeKeyPattern, s.workerID)
	reportedTime := strconv.FormatInt(s.generator.LastUsedTimestamp(), 10)

	ctx, cancel := context.WithTimeout(context.Background(), etcdCommandTimeout)
	defer cancel()

	commit, err := s.etcdClient.Txn(ctx).If(
		clientv3.Compare(clientv3.LeaseValue(liveServerKey), "=", s.leaseID),
	).Then(
		clientv3.OpPut(reportedTimeKey, reportedTime),
	).Else(
		clientv3.OpGet(liveServerKey),
	).Commit()

	if err != nil || !commit.Succeeded || len(commit.Responses) != 1 {
		zap.S().Errorf("cannot report last used timestamp, %v, %+v", err, commit)
		return false
	}

	op := commit.Responses[0]
	if op.GetResponseRange() != nil {
		zap.S().Errorf("lease id of key %d is differernt from server's lease id %d",
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

// Stop stops the snowflake server. This is public method so that it can be called, e.g. called by the main thread on app exit.
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

// isServerIDAvailable checks whether the current server can claim a server ID.
func (s *Worker) isServerIDAvailable(ctx context.Context, id int) bool {
	k := fmt.Sprintf(liveServerKeyPattern, id)
	resp, err := s.etcdClient.Get(ctx, k)
	if err != nil {
		zap.S().Error(err)
		return false
	}
	if resp.Count > 0 {
		zap.S().Errorf("server id %d is in use", id)
		return false
	}

	k = fmt.Sprintf("%s/%d", lastReportedTimeKeyPattern, id)
	resp, err = s.etcdClient.Get(ctx, k)
	if err != nil {
		zap.S().Error(err)
		return false
	}
	// The server-id has been used by some server previously. But the server has stopped using the ID for some reason.
	// If the current milliseconds are larger than (the last reported milliseconds of the stopped server + heartbeatReportPeriod),
	// then the current server can reuse the server id.
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

// claimServerID takes the server id. Etcd transactions are serializable which guarantees
// no two servers can take the same ID.
func (s *Worker) claimServerID(ctx context.Context, id int) bool {
	k := fmt.Sprintf(liveServerKeyPattern, id)

	commit, err := s.etcdClient.Txn(ctx).If(
		clientv3.Compare(clientv3.Version(k), "=", 0),
	).Then(
		clientv3.OpPut(k, strconv.FormatInt(time.Now().UnixMilli(), 10), clientv3.WithLease(s.leaseID)),
	).Else(
		clientv3.OpGet(k),
	).Commit()
	if err != nil || !commit.Succeeded || len(commit.Responses) != 1 {
		zap.S().Warnf("cannot claim server id in a transaction, %v, %+v", err, commit)
		return false
	}

	op := commit.Responses[0]
	if op.GetResponseRange() != nil {
		zap.S().Warnf("Worker id already claimed by another server with lease id %d", op.GetResponseRange().Kvs[0].Lease)
		return false
	}
	if op.GetResponsePut() == nil {
		zap.S().Errorf("Unrecognized claim server id transaction response. %+v. It should either be a Get or Put.", op)
		return false
	}
	zap.S().Infof("Successfully claimed server id, %d", id)
	return true
}

// StopChannel returns a channel that notifies when the snowflake server has stopped.
func (s *Worker) StopChannel() <-chan struct{} {
	return s.stopChan
}

func (s *Worker) Ready() bool {
	return s.state == stateReady
}

func (s *Worker) Live() bool {
	return s.state != stateStopped
}

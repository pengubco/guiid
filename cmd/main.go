package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/pengubco/guiid/api/v1"
	"github.com/pengubco/guiid/worker"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const (
	// the listening port of the grpc snowflake id server. 7669 is "SNOW" on old touch-tone keypad.
	grpcPort = 7669

	// the listening port for Prometheus metric, pprof and probing (/ready, /live)
	metricPort = 8001
)

var (
	// The server that generates snowflake IDs.
	snowflakeServer *Server

	// The grpc server that handles gRPC protocol for the snowflake ID server.
	grpcServer *grpc.Server

	// A http server that hosts the prometheus metrics and probing endpoint
	metricsServer *http.Server

	logger *zap.Logger

	configFromFile = flag.String("config_from_file", "", "location of config file in local filesystem.")
)

func main() {
	initZapLogging()
	flag.Parse()

	c, err := loadConfigurationFromFile(*configFromFile)
	if err != nil {
		zap.S().Errorf("cannot load config from file. %v", err)
		return
	}
	w, err := worker.NewWorker(c.EtcdEndpoints, c.EtcdPrefix, c.EtcdUsername, c.EtcdPassword, c.MaxServerCnt)
	if err != nil {
		zap.S().Error(w)
		return
	}
	snowflakeServer = &Server{w: w}

	setupGRPCServer()
	go func() {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
		if err != nil {
			zap.S().Errorf("failed to listen: %v", err)
		}
		if err = grpcServer.Serve(l); err != nil {
			zap.S().Errorf("failed to serve: %v", err)
		}
	}()

	setupMetricsServer()
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && errors.Is(err, http.ErrServerClosed) {
			zap.S().Errorf("metrics server listen error %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case v := <-quit:
			zap.S().Infof("shutdown on signal %v", v)
			stop()
			snowflakeServer.w.Stop()
			return
		// no point of continue if the snowflake id server has stopped.
		case <-snowflakeServer.w.StopChannel():
			zap.S().Infof("shutdown because the snowflake id server has stopped")
			stop()
			return
		}
	}
}

func setupGRPCServer() {
	grpcServer = grpc.NewServer()
	pb.RegisterSnowflakeIDServer(grpcServer, snowflakeServer)
}

func setupMetricsServer() {
	addr := fmt.Sprintf(":%d", metricPort)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if snowflakeServer.w.Ready() {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(400)
		}
	})

	metricsServer = &http.Server{
		ReadHeaderTimeout: time.Millisecond,
		Addr:              addr,
		Handler:           mux,
	}
}

func initZapLogging() {
	logger, _ = zap.NewDevelopment()
	defer func() {
		if err := logger.Sync(); err != nil {
			fmt.Printf("logger sync error: %v\n", err)
		}
	}()
	zap.ReplaceGlobals(logger)
}

func stop() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	grpcServer.Stop()

	if err := metricsServer.Shutdown(ctx); err != nil {
		zap.S().Errorf("shutdown metrics server, %v", err)
	}
}

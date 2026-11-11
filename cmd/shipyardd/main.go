// Command shipyardd serves the Shipyard HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/api"
	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/config"
	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/metrics"
	"github.com/gabbygtobar-coder/shipyard/internal/proxy"
	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	// writeTimeout is disabled so log streams are not cut off. Build work is
	// bounded by the build timeout, and clients bound how long they read.
	writeTimeout    = 0
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 20 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	loaded, cfgErr := config.Load()
	flagDefaults := loaded
	if cfgErr != nil {
		flagDefaults = config.Defaults()
	}

	fs := flag.NewFlagSet("shipyardd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", flagDefaults.Addr, "listen address (env "+config.EnvAddr+")")
	concurrency := fs.Int("build-concurrency", flagDefaults.BuildConcurrency, "parallel image builds (env "+config.EnvBuildConcurrency+")")
	queueSize := fs.Int("build-queue-size", flagDefaults.BuildQueueSize, "builds that may wait for a worker (env "+config.EnvBuildQueueSize+")")
	buildTimeout := fs.Duration("build-timeout", flagDefaults.BuildTimeout, "time limit for clone and docker build (env "+config.EnvBuildTimeout+")")
	cloneTimeout := fs.Duration("clone-timeout", flagDefaults.CloneTimeout, "time limit for git clone (env "+config.EnvCloneTimeout+")")
	cloneMax := fs.Int64("clone-max-bytes", flagDefaults.CloneMaxBytes, "maximum cloned repository size in bytes (env "+config.EnvCloneMaxBytes+")")
	proxyAddr := fs.String("proxy-addr", flagDefaults.ProxyAddr, "reverse proxy listen address (env "+config.EnvProxyAddr+")")
	containerPort := fs.Int("container-port", flagDefaults.ContainerPort, "port inside the app container (env "+config.EnvContainerPort+")")
	memoryBytes := fs.Int64("memory-bytes", flagDefaults.MemoryBytes, "app container memory limit in bytes (env "+config.EnvMemoryBytes+")")
	cpus := fs.Float64("cpus", float64(flagDefaults.CPUMilli)/1000, "app container CPU limit (env "+config.EnvCPUs+")")
	healthPath := fs.String("health-path", flagDefaults.HealthPath, "HTTP health path (env "+config.EnvHealthPath+")")
	healthTimeout := fs.Duration("health-timeout", flagDefaults.HealthTimeout, "health gate time limit (env "+config.EnvHealthTimeout+")")
	healthInterval := fs.Duration("health-interval", flagDefaults.HealthInterval, "delay between health probes (env "+config.EnvHealthInterval+")")
	network := fs.String("runtime-network", flagDefaults.RuntimeNetwork, "docker network for app containers; empty publishes on 127.0.0.1 (env "+config.EnvRuntimeNetwork+")")
	dbPath := fs.String("db", flagDefaults.DBPath, "sqlite database file, or memory (env "+config.EnvDB+")")
	releaseHistory := fs.Int("release-history", flagDefaults.ReleaseHistory, "successful images kept per app (env "+config.EnvReleaseHistory+")")
	runtimeName := fs.String("runtime", flagDefaults.Runtime, "docker or ecs (env "+config.EnvRuntime+")")
	awsRegion := fs.String("aws-region", flagDefaults.AWSRegion, "AWS region for the ecs runtime (env "+config.EnvAWSRegion+")")
	ecsCluster := fs.String("ecs-cluster", flagDefaults.ECSCluster, "ECS cluster name (env "+config.EnvECSCluster+")")
	ecrRepository := fs.String("ecr-repository", flagDefaults.ECRRepository, "ECR repository URL without a scheme (env "+config.EnvECRRepository+")")
	executionRole := fs.String("ecs-execution-role-arn", flagDefaults.ECSExecutionRoleARN, "Fargate execution role ARN (env "+config.EnvECSExecutionRoleARN+")")
	taskRole := fs.String("ecs-task-role-arn", flagDefaults.ECSTaskRoleARN, "Fargate task role ARN (env "+config.EnvECSTaskRoleARN+")")
	ecsSubnets := fs.String("ecs-subnets", strings.Join(flagDefaults.ECSSubnets, ","), "comma-separated public subnet ids (env "+config.EnvECSSubnets+")")
	ecsSecurityGroups := fs.String("ecs-security-groups", strings.Join(flagDefaults.ECSSecurityGroups, ","), "comma-separated task security group ids (env "+config.EnvECSSecurityGroups+")")
	ecsLogGroup := fs.String("ecs-log-group", flagDefaults.ECSLogGroup, "CloudWatch log group (env "+config.EnvECSLogGroup+")")
	albDNS := fs.String("alb-dns-name", flagDefaults.ALBDNSName, "ALB DNS name without a scheme (env "+config.EnvALBDNSName+")")
	baseDomain := fs.String("base-domain", flagDefaults.BaseDomain, "optional parent domain for host rules (env "+config.EnvBaseDomain+")")
	ecsDeployTimeout := fs.Duration("ecs-deploy-timeout", flagDefaults.ECSDeployTimeout, "ECS rollout time limit (env "+config.EnvECSDeployTimeout+")")
	monitorInterval := fs.Duration("monitor-interval", flagDefaults.MonitorInterval, "how often to probe running apps (env "+config.EnvMonitorInterval+")")
	monitorThreshold := fs.Int("monitor-threshold", flagDefaults.MonitorThreshold, "consecutive probe failures before an app is unhealthy (env "+config.EnvMonitorThreshold+")")
	autoRestart := fs.Bool("auto-restart", flagDefaults.AutoRestart, "restart a local container after repeated health failures (env "+config.EnvAutoRestart+")")
	fs.Usage = func() {
		fprintf(fs.Output(), "Usage: shipyardd [--addr %s] [--proxy-addr %s]\n", config.DefaultAddr, config.DefaultProxyAddr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fprintln(stderr, "unexpected arguments")
		fs.Usage()
		return 2
	}
	if cfgErr != nil {
		fprintf(stderr, "config: %s\n", cfgErr)
		return 2
	}
	cfg := config.Config{
		Addr:                *addr,
		ProxyAddr:           *proxyAddr,
		BuildConcurrency:    *concurrency,
		BuildQueueSize:      *queueSize,
		BuildTimeout:        *buildTimeout,
		CloneTimeout:        *cloneTimeout,
		CloneMaxBytes:       *cloneMax,
		ContainerPort:       *containerPort,
		MemoryBytes:         *memoryBytes,
		CPUMilli:            int(math.Round(*cpus * 1000)),
		HealthPath:          *healthPath,
		HealthTimeout:       *healthTimeout,
		HealthInterval:      *healthInterval,
		RuntimeNetwork:      *network,
		DBPath:              *dbPath,
		ReleaseHistory:      *releaseHistory,
		WebhookSecret:       loaded.WebhookSecret,
		Runtime:             strings.TrimSpace(*runtimeName),
		AWSRegion:           strings.TrimSpace(*awsRegion),
		ECSCluster:          strings.TrimSpace(*ecsCluster),
		ECRRepository:       strings.TrimSpace(*ecrRepository),
		ECSExecutionRoleARN: strings.TrimSpace(*executionRole),
		ECSTaskRoleARN:      strings.TrimSpace(*taskRole),
		ECSSubnets:          splitCSV(*ecsSubnets),
		ECSSecurityGroups:   splitCSV(*ecsSecurityGroups),
		ECSLogGroup:         strings.TrimSpace(*ecsLogGroup),
		ALBDNSName:          strings.TrimSpace(*albDNS),
		BaseDomain:          strings.TrimSpace(*baseDomain),
		ECSDeployTimeout:    *ecsDeployTimeout,
		MonitorInterval:     *monitorInterval,
		MonitorThreshold:    *monitorThreshold,
		AutoRestart:         *autoRestart,
		UIToken:             loaded.UIToken,
	}
	if err := cfg.Check(); err != nil {
		fprintf(stderr, "config: %s\n", err)
		return 2
	}

	st, closeStore, err := openStore(cfg.DBPath)
	if err != nil {
		logger.Error("database", "err", err)
		return 1
	}
	defer closeStore()
	routes := route.NewTable()
	wired, err := wireRuntime(context.Background(), cfg, logger, st, routes)
	if err != nil {
		logger.Error("runtime", "err", err)
		return 1
	}
	supervisor, err := runtime.NewSupervisor(runtime.SupervisorConfig{
		Store:         st,
		Runtime:       wired.rt,
		Checker:       wired.checker,
		Routes:        routes,
		Port:          cfg.ContainerPort,
		MemoryBytes:   cfg.MemoryBytes,
		CPUMilli:      cfg.CPUMilli,
		Network:       wired.network,
		HealthTimeout: cfg.HealthTimeout,
		StartTimeout:  wired.startTimeout,
		PublicURL:     wired.publicURL,
		Logger:        logger,
	})
	if err != nil {
		logger.Error("runtime", "err", err)
		return 1
	}
	collected, err := metrics.New(nil, st.CountActiveDeployments)
	if err != nil {
		logger.Error("metrics", "err", err)
		return 1
	}
	runner, err := deploy.New(deploy.Config{
		Store:          st,
		Cloner:         source.NewGit(cfg.CloneMaxBytes),
		Builder:        build.NewDocker(),
		Releaser:       supervisor,
		Concurrency:    cfg.BuildConcurrency,
		QueueSize:      cfg.BuildQueueSize,
		BuildTimeout:   cfg.BuildTimeout,
		CloneTimeout:   cfg.CloneTimeout,
		ReleaseHistory: cfg.ReleaseHistory,
		Images:         wired.images,
		Metrics:        collected,
		Logger:         logger,
	})
	if err != nil {
		logger.Error("build runner", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var restarter runtime.Restarter
	if cfg.AutoRestart {
		if local, ok := wired.rt.(runtime.Restarter); ok {
			restarter = local
		} else {
			logger.Info("auto-restart unavailable", "runtime", cfg.Runtime)
		}
	}
	probeTimeout := 2 * time.Second
	if cfg.HealthTimeout < probeTimeout {
		probeTimeout = cfg.HealthTimeout
	}
	monitor, err := runtime.NewMonitor(runtime.MonitorConfig{
		Store:        st,
		Routes:       routes,
		Path:         cfg.HealthPath,
		Interval:     cfg.MonitorInterval,
		Threshold:    cfg.MonitorThreshold,
		AutoRestart:  cfg.AutoRestart,
		Restarter:    restarter,
		ProbeTimeout: probeTimeout,
		Logger:       logger,
	})
	if err != nil {
		logger.Error("health monitor", "err", err)
		return 1
	}
	if err := runner.Start(ctx); err != nil {
		logger.Error("start builds", "err", err)
		return 1
	}
	defer runner.Stop()

	var ready api.ReadyCheck
	if check, ok := wired.rt.(api.ReadyCheck); ok {
		ready = check
	}
	appProxy := proxy.New(routes, logger)
	appProxy.SetObserver(collected)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewHandler(st, runner, logger, supervisor, api.WithWebhookSecret(cfg.WebhookSecret), api.WithMetrics(collected), api.WithReadyCheck(ready), api.WithUIToken(cfg.UIToken)),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	proxySrv := &http.Server{
		Addr:              cfg.ProxyAddr,
		Handler:           appProxy,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 2)
	go monitor.Run(ctx)
	go func() {
		logger.Info("listening",
			"addr", cfg.Addr,
			"proxy_addr", cfg.ProxyAddr,
			"build_concurrency", cfg.BuildConcurrency,
			"db", cfg.DBPath,
			"runtime", cfg.Runtime,
			"webhook", cfg.WebhookSecret != "",
			"ui", cfg.UIToken != "",
			"auto_restart", cfg.AutoRestart && restarter != nil,
		)
		errCh <- srv.ListenAndServe()
	}()
	go func() {
		errCh <- proxySrv.ListenAndServe()
	}()

	shutdown := func() int {
		runner.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		supervisor.Shutdown(shutdownCtx)
		errAPI := srv.Shutdown(shutdownCtx)
		errProxy := proxySrv.Shutdown(shutdownCtx)
		if errAPI != nil || errProxy != nil {
			logger.Error("shutdown", "api", errAPI, "proxy", errProxy)
			return 1
		}
		return 0
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			_ = shutdown()
			return 1
		}
	case <-ctx.Done():
		return shutdown()
	}
	return 0
}

func openStore(path string) (store.Store, func(), error) {
	if path == config.MemoryDBPath {
		return store.NewMemory(), func() {}, nil
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = db.Close() }, nil
}

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func fprintln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

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
	"syscall"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/api"
	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/config"
	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
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
		Addr:             *addr,
		ProxyAddr:        *proxyAddr,
		BuildConcurrency: *concurrency,
		BuildQueueSize:   *queueSize,
		BuildTimeout:     *buildTimeout,
		CloneTimeout:     *cloneTimeout,
		CloneMaxBytes:    *cloneMax,
		ContainerPort:    *containerPort,
		MemoryBytes:      *memoryBytes,
		CPUMilli:         int(math.Round(*cpus * 1000)),
		HealthPath:       *healthPath,
		HealthTimeout:    *healthTimeout,
		HealthInterval:   *healthInterval,
		RuntimeNetwork:   *network,
	}
	if err := cfg.Check(); err != nil {
		fprintf(stderr, "config: %s\n", err)
		return 2
	}

	st := store.NewMemory()
	dockerRT := runtime.NewDocker()
	reapCtx, reapCancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := dockerRT.StopManaged(reapCtx); err != nil && !errors.Is(err, runtime.ErrDockerNotFound) {
		logger.Error("stop old containers", "err", err)
	}
	reapCancel()

	routes := route.NewTable()
	supervisor, err := runtime.NewSupervisor(runtime.SupervisorConfig{
		Store:   st,
		Runtime: dockerRT,
		Checker: runtime.HTTPChecker{
			Path:     cfg.HealthPath,
			Interval: cfg.HealthInterval,
		},
		Routes:        routes,
		Port:          cfg.ContainerPort,
		MemoryBytes:   cfg.MemoryBytes,
		CPUMilli:      cfg.CPUMilli,
		Network:       cfg.RuntimeNetwork,
		HealthTimeout: cfg.HealthTimeout,
		PublicURL: func(app string) string {
			appURL, urlErr := proxy.PublicURL(cfg.ProxyAddr, app)
			if urlErr != nil {
				return ""
			}
			return appURL
		},
		Logger: logger,
	})
	if err != nil {
		logger.Error("runtime", "err", err)
		return 1
	}
	runner, err := deploy.New(deploy.Config{
		Store:        st,
		Cloner:       source.NewGit(cfg.CloneMaxBytes),
		Builder:      build.NewDocker(),
		Releaser:     supervisor,
		Concurrency:  cfg.BuildConcurrency,
		QueueSize:    cfg.BuildQueueSize,
		BuildTimeout: cfg.BuildTimeout,
		CloneTimeout: cfg.CloneTimeout,
		Logger:       logger,
	})
	if err != nil {
		logger.Error("build runner", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runner.Start(ctx); err != nil {
		logger.Error("start builds", "err", err)
		return 1
	}
	defer runner.Stop()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewHandler(st, runner, logger, supervisor),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	proxySrv := &http.Server{
		Addr:              cfg.ProxyAddr,
		Handler:           proxy.New(routes, logger),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("listening", "addr", cfg.Addr, "proxy_addr", cfg.ProxyAddr, "build_concurrency", cfg.BuildConcurrency)
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

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func fprintln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/config"
	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/proxy"
	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime/ecs"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

type wiredRuntime struct {
	rt           runtime.Runtime
	checker      runtime.Checker
	images       deploy.ImageRemover
	network      string
	startTimeout time.Duration
	publicURL    func(string) string
}

func wireRuntime(ctx context.Context, cfg config.Config, logger *slog.Logger, st store.Store, routes *route.Table) (wiredRuntime, error) {
	switch cfg.Runtime {
	case config.RuntimeDocker:
		return wireDocker(ctx, cfg, logger, st)
	case config.RuntimeECS:
		return wireECS(ctx, cfg, logger, st, routes)
	default:
		return wiredRuntime{}, errors.New("unknown runtime")
	}
}

func wireDocker(ctx context.Context, cfg config.Config, logger *slog.Logger, st store.Store) (wiredRuntime, error) {
	if err := runtime.ReconcileLocalRuntime(ctx, st); err != nil {
		return wiredRuntime{}, err
	}
	dockerRT := runtime.NewDocker()
	reapCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := dockerRT.StopManaged(reapCtx)
	cancel()
	if err != nil && !errors.Is(err, runtime.ErrDockerNotFound) {
		logger.Error("stop old containers", "err", err)
	}
	return wiredRuntime{
		rt:      dockerRT,
		checker: runtime.HTTPChecker{Path: cfg.HealthPath, Interval: cfg.HealthInterval},
		images:  dockerRT,
		network: cfg.RuntimeNetwork,
		publicURL: func(app string) string {
			appURL, urlErr := proxy.PublicURL(cfg.ProxyAddr, app)
			if urlErr != nil {
				return ""
			}
			return appURL
		},
	}, nil
}

func wireECS(ctx context.Context, cfg config.Config, logger *slog.Logger, st store.Store, routes *route.Table) (wiredRuntime, error) {
	if err := runtime.ReconcileInterruptedBuilds(ctx, st); err != nil {
		return wiredRuntime{}, err
	}
	ecsRT, err := ecs.New(ecs.Config{
		Region:           cfg.AWSRegion,
		Cluster:          cfg.ECSCluster,
		Repository:       cfg.ECRRepository,
		ExecutionRoleARN: cfg.ECSExecutionRoleARN,
		TaskRoleARN:      cfg.ECSTaskRoleARN,
		Subnets:          cfg.ECSSubnets,
		SecurityGroups:   cfg.ECSSecurityGroups,
		LogGroup:         cfg.ECSLogGroup,
		ALBDNSName:       cfg.ALBDNSName,
		BaseDomain:       cfg.BaseDomain,
		Logger:           logger,
	})
	if err != nil {
		return wiredRuntime{}, err
	}
	if err := ecs.RestoreRoutes(ctx, st, routes); err != nil {
		return wiredRuntime{}, err
	}
	return wiredRuntime{
		rt:           ecsRT,
		checker:      runtime.PassChecker{},
		images:       ecsRT,
		startTimeout: cfg.ECSDeployTimeout,
		publicURL: func(app string) string {
			appURL, urlErr := ecs.PublicURL(cfg.ALBDNSName, cfg.BaseDomain, app)
			if urlErr != nil {
				return ""
			}
			return appURL
		},
	}, nil
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

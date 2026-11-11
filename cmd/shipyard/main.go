// Command shipyard is the command-line client for shipyardd.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/config"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	commandTimeout = 15 * time.Second
	usageText      = `Shipyard registers applications and builds images with a shipyardd API.

Usage:
  shipyard [--api-url URL] apps create --name NAME --repo URL
  shipyard [--api-url URL] apps list
  shipyard [--api-url URL] apps get NAME
  shipyard [--api-url URL] deploy APP [--ref REF]
  shipyard [--api-url URL] deployments list APP
  shipyard [--api-url URL] logs DEPLOYMENT_ID [--follow]

The API base URL is taken from --api-url, then SHIPYARD_API_URL,
then http://127.0.0.1:8080. An empty --ref builds the repository default branch.
logs --follow streams until the build finishes and exits 1 when it fails.

`
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	fs := flag.NewFlagSet("shipyard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", "", "Shipyard API base URL (overrides "+config.EnvAPIURL+")")
	fs.Usage = func() {
		fprintf(fs.Output(), "%s", usageText)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	base := strings.TrimSpace(*apiURL)
	if base == "" {
		base = strings.TrimSpace(getenv(config.EnvAPIURL))
	}
	if base == "" {
		base = config.DefaultAPIURL
	}
	client := newAPIClient(base)
	switch rest[0] {
	case "apps":
		return runApps(rest[1:], client, stdout, stderr)
	case "deploy":
		return runDeploy(rest[1:], client, stdout, stderr)
	case "deployments":
		return runDeployments(rest[1:], client, stdout, stderr)
	case "logs":
		return runLogs(rest[1:], client, stdout, stderr)
	default:
		fprintf(stderr, "unknown command %q\n\n", rest[0])
		fs.Usage()
		return 2
	}
}

func runApps(args []string, client *apiClient, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fprintln(stderr, "usage: shipyard apps create|list|get")
		return 2
	}
	switch args[0] {
	case "create":
		return runAppsCreate(args[1:], client, stdout, stderr)
	case "list":
		return runAppsList(args[1:], client, stdout, stderr)
	case "get":
		return runAppsGet(args[1:], client, stdout, stderr)
	default:
		fprintf(stderr, "unknown apps command %q\n", args[0])
		return 2
	}
}

func runAppsCreate(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apps create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "application name")
	repo := fs.String("repo", "", "https GitHub repository URL")
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard apps create --name NAME --repo URL")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || strings.TrimSpace(*name) == "" || strings.TrimSpace(*repo) == "" {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	app, err := client.createApp(ctx, strings.TrimSpace(*name), strings.TrimSpace(*repo))
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, app)
}

func runAppsList(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apps list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard apps list")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	apps, err := client.listApps(ctx)
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, apps)
}

func runAppsGet(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apps get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard apps get NAME")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	app, err := client.getApp(ctx, fs.Arg(0))
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, app)
}

func runDeploy(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ref := fs.String("ref", "", "git branch, tag, or commit (default branch if empty)")
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard deploy APP [--ref REF]")
		fs.PrintDefaults()
	}
	app, flagArgs := splitLeadingArg(args)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if app == "" && fs.NArg() == 1 {
		app = fs.Arg(0)
	} else if app != "" && fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	app = strings.TrimSpace(app)
	if app == "" {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	dep, err := client.deploy(ctx, app, strings.TrimSpace(*ref))
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, dep)
}

func runDeployments(args []string, client *apiClient, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fprintln(stderr, "usage: shipyard deployments list APP")
		return 2
	}
	switch args[0] {
	case "list":
		return runDeploymentsList(args[1:], client, stdout, stderr)
	default:
		fprintf(stderr, "unknown deployments command %q\n", args[0])
		return 2
	}
}

func runDeploymentsList(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deployments list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard deployments list APP")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	list, err := client.listDeployments(ctx, strings.TrimSpace(fs.Arg(0)))
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, list)
}

func runLogs(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	follow := fs.Bool("follow", false, "stream until the build finishes")
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard logs DEPLOYMENT_ID [--follow]")
		fs.PrintDefaults()
	}
	id, flagArgs := splitLeadingArg(args)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	} else if id != "" && fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	id = strings.TrimSpace(id)
	if id == "" {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	if *follow {
		cancel()
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()
	if err := client.streamLogs(ctx, id, *follow, stdout); err != nil {
		return writeCLIError(stderr, err)
	}
	if !*follow {
		return 0
	}
	statusCtx, statusCancel := context.WithTimeout(context.Background(), commandTimeout)
	defer statusCancel()
	dep, err := client.getDeployment(statusCtx, id)
	if err != nil {
		return writeCLIError(stderr, err)
	}
	if dep.Status != store.DeploymentStatusSucceeded {
		if dep.Error != "" {
			return writeCLIError(stderr, errors.New(dep.Error))
		}
		return writeCLIError(stderr, errors.New("deployment "+dep.Status))
	}
	return 0
}

// splitLeadingArg peels a positional argument off the front so flags that
// follow it are still parsed. Go's flag package stops at the first non-flag.
func splitLeadingArg(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return args[0], args[1:]
}

func writeJSON(w io.Writer, stderr io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return writeCLIError(stderr, err)
	}
	return 0
}

func writeCLIError(stderr io.Writer, err error) int {
	fprintf(stderr, "error: %s\n", err)
	return 1
}

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func fprintln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

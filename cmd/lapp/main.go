package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lapp-dev/lapp/internal/fileio"
	"github.com/lapp-dev/lapp/internal/server"
)

// buildVersion is set by goreleaser ldflags: -X main.buildVersion={{.Version}}
var buildVersion = "dev"

// multiFlag is a custom flag.Value for repeatable string flags (e.g. --block, --allow).
type multiFlag []string

func (m *multiFlag) String() string        { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error    { *m = append(*m, v); return nil }

func mustGetwd() string {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lapp: cannot get working directory: %v\n", err)
		os.Exit(1)
	}
	return dir
}

func main() {
	// Flags.
	rootFlag    := flag.String("root",     envOr("LAPP_ROOT", mustGetwd()), "Restrict file operations to this directory tree")
	limitFlag   := flag.Int("limit",       envInt("LAPP_LIMIT", 2000),       "Default max lines returned by lapp_read")
	logFileFlag := flag.String("log-file", envOr("LAPP_LOG_FILE", ""),       "Write server logs here (default: stderr)")
	versionFlag := flag.Bool("version",    false,                             "Print version and exit")

	var blockPatterns multiFlag
	var allowPatterns multiFlag
	flag.Var(&blockPatterns, "block", "Add a path pattern to the block list (repeatable)")
	flag.Var(&allowPatterns, "allow", "Remove a pattern from the block list (repeatable)")

	flag.Parse()

	if *versionFlag {
		fmt.Println(buildVersion)
		return
	}

	// Configure log output.
	if logPath := *logFileFlag; logPath != "" {
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lapp: cannot open log file %q: %v\n", logPath, err)
			os.Exit(1)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	// Resolve root to canonical path.
	root, err := filepath.EvalSymlinks(filepath.Clean(*rootFlag))
	if err != nil {
		fmt.Fprintf(os.Stderr, "lapp: invalid root %q: %v\n", *rootFlag, err)
		os.Exit(1)
	}

	// Build block/allow lists: defaults first, then CLI overrides.
	blocks := append(fileio.DefaultBlockPatterns, blockPatterns...)
	allows := append(fileio.DefaultAllowPatterns, allowPatterns...)

	// Apply LAPP_BLOCK and LAPP_ALLOW env vars (colon-separated).
	if v := os.Getenv("LAPP_BLOCK"); v != "" {
		for _, p := range strings.Split(v, ":") {
			if p != "" {
				blocks = append(blocks, p)
			}
		}
	}
	if v := os.Getenv("LAPP_ALLOW"); v != "" {
		for _, p := range strings.Split(v, ":") {
			if p != "" {
				allows = append(allows, p)
			}
		}
	}

	cfg := &fileio.Config{
		Root:          root,
		BlockPatterns: blocks,
		AllowPatterns: allows,
		DefaultLimit:  *limitFlag,
	}

	// Startup: remove orphaned *.lapp.tmp files older than 5 minutes (§9.1).
	fileio.CleanupOrphans(root)
	
	if err := server.New(cfg).Start(); err != nil {
		fmt.Fprintf(os.Stderr, "lapp: server error: %v\n", err)
		os.Exit(1)
	}
}

// envOr returns the environment variable value or fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt returns the env var as int, or fallback on missing/invalid.
func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

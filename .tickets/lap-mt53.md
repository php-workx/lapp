---
id: lap-mt53
status: closed
deps: [lap-nelu]
links: []
created: 2026-04-04T04:17:40Z
type: task
priority: 2
assignee: Ronny Unger
tags: [wave-5, lapp]
---
# Issue 7: cmd/lapp entrypoint + complete goreleaser config

Implement main.go with all flags from §13. Complete .goreleaser.yml for cross-platform release.

cmd/lapp/main.go:
  func main() {
      root    := flag.String("root", mustGetwd(), "restrict file ops to this directory tree")
      limit   := flag.Int("limit", 2000, "default max lines for lapp_read")
      logFile := flag.String("log-file", "", "log destination (default stderr)")
      version := flag.Bool("version", false, "print version and exit")
      // --block and --allow as repeatable multi-value flags (custom flag.Value)
      flag.Parse()
      if *version { fmt.Println(buildVersion); return }
      // configure log output
      // build fileio.Config
      // server.New(cfg).Start()
  }

buildVersion set via goreleaser ldflags: -X main.buildVersion={{.Version}}

Environment variables (§13) — all flags also accept env vars; flag takes precedence over env var:
  LAPP_ROOT      → --root
  LAPP_LIMIT     → --limit
  LAPP_BLOCK     → --block (colon-separated list maps to multiple --block values)
  LAPP_ALLOW     → --allow (colon-separated list maps to multiple --allow values)
  LAPP_LOG_FILE  → --log-file

.goreleaser.yml:
  builds:
    - id: lapp
      main: ./cmd/lapp
      binary: lapp
      env: [CGO_ENABLED=0]
      goos: [linux, darwin, windows]
      goarch: [amd64, arm64]
      ignore:
        - {goos: windows, goarch: arm64}
      ldflags: ["-s -w -X main.buildVersion={{.Version}}"]
  archives:
    - format: tar.gz
      format_overrides: [{goos: windows, format: zip}]

## Acceptance Criteria

go build -o /tmp/lapp-test ./cmd/lapp && /tmp/lapp-test --version works; goreleaser release --snapshot --clean --skip=publish succeeds


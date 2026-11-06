// archive-probe performs opt-in PUT/GET/hash/DELETE against configured targets.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/objectstore"
	"os"
	"time"
)

func main() {
	path := flag.String("config", "", "archive JSON config file (credentials stay local)")
	targetID := flag.String("target", "", "probe only this configured target ID")
	write := flag.Bool("write-probe", false, "authorize creating and deleting one tiny diagnostics object per target")
	flag.Parse()
	var err error
	raw := []byte(os.Getenv("CONTINUITY_ARCHIVE_CONFIG"))
	if *path == "" {
		*path = os.Getenv("CONTINUITY_ARCHIVE_CONFIG_FILE")
	}
	if *path != "" {
		raw, err = os.ReadFile(*path)
	}
	if err != nil || len(raw) == 0 {
		fmt.Fprintln(os.Stderr, "cannot read config")
		os.Exit(1)
	}
	var config struct {
		Targets []objectstore.Target `json:"targets"`
	}
	if json.Unmarshal(raw, &config) != nil || len(config.Targets) == 0 {
		fmt.Fprintln(os.Stderr, "invalid config/targets")
		os.Exit(1)
	}
	matched := false
	for _, target := range config.Targets {
		if *targetID != "" && target.ID != *targetID {
			continue
		}
		matched = true
		p, err := objectstore.New(target)
		if err != nil {
			fmt.Fprintln(os.Stderr, "target initialization failed (check provider config and secret files)")
			os.Exit(1)
		}
		if !*write {
			fmt.Printf("%s/%s: configuration valid; no network probe performed\n", target.ID, target.Provider)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		key, err := objectstore.Probe(ctx, p, target)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v; owned probe key=%s (check/clean up this key only)\n", target.ID, err, key)
			os.Exit(1)
		}
		fmt.Printf("%s/%s: PUT → GET → SHA256 → DELETE passed; key=%s (versioned buckets may retain versions)\n", target.ID, target.Provider, key)
	}
	if !matched {
		fmt.Fprintln(os.Stderr, "target not found")
		os.Exit(2)
	}
}

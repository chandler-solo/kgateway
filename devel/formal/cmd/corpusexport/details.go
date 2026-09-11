package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Detail capture is separate from inventory completion. A title/body inventory
// never implies comments, review discussion, fixes, or release ancestry were read.
func exportDetails(root, path string, cfg config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest struct {
		Entries []struct {
			Repository string `json:"repository"`
			Number     int    `json:"number"`
		} `json:"entries"`
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if len(manifest.Entries) == 0 {
		return errors.New("empty detail manifest")
	}
	allowed := map[string]bool{}
	for _, repo := range cfg.Repositories {
		allowed[repo] = true
	}
	for _, entry := range manifest.Entries {
		if !allowed[entry.Repository] || entry.Number < 1 {
			return errors.New("detail entry outside scope")
		}
		dir := filepath.Join(root, strings.ReplaceAll(entry.Repository, "/", "__"), fmt.Sprintf("details-%d", entry.Number))
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		base := fmt.Sprintf("repos/%s/issues/%d", entry.Repository, entry.Number)
		issue, err := cached(filepath.Join(dir, "issue.json"), base)
		if err != nil {
			return err
		}
		var kind struct {
			PR json.RawMessage `json:"pull_request"`
		}
		if err = json.Unmarshal(issue.Data, &kind); err != nil {
			return err
		}
		endpoints := map[string]string{"comments": base + "/comments?per_page=100", "events": base + "/events?per_page=100"}
		if len(kind.PR) > 0 {
			pull := fmt.Sprintf("repos/%s/pulls/%d", entry.Repository, entry.Number)
			if _, err = cached(filepath.Join(dir, "pull.json"), pull); err != nil {
				return err
			}
			endpoints["files"] = pull + "/files?per_page=100"
			endpoints["commits"] = pull + "/commits?per_page=100"
			endpoints["review-comments"] = pull + "/comments?per_page=100"
			endpoints["reviews"] = pull + "/reviews?per_page=100"
		}
		for kind, endpoint := range endpoints {
			seen := map[string]bool{}
			for page := 1; endpoint != ""; page++ {
				if seen[endpoint] {
					return errors.New("detail cursor cycle")
				}
				seen[endpoint] = true
				r, err := cached(filepath.Join(dir, fmt.Sprintf("%s-%05d.json", kind, page)), endpoint)
				if err != nil {
					return err
				}
				endpoint = r.Next
			}
		}
		fmt.Printf("%s#%d: detail pages captured; linked-source/release-ancestry review remains separate\n", entry.Repository, entry.Number)
	}
	return nil
}

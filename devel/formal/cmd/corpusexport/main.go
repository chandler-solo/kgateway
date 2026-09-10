// corpusexport freezes a read-only issue/PR inventory outside the repository.
// It enumerates REST pages, avoiding the GitHub search result cap. A keyword
// hit is a candidate, never a verified bug or a complete scope denominator.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type config struct {
	Cutoff       string   `json:"cutoff"`
	Repositories []string `json:"repositories"`
	Terms        []string `json:"terms"`
}
type issue struct {
	ID       int64           `json:"id"`
	Number   int             `json:"number"`
	URL      string          `json:"html_url"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	Created  time.Time       `json:"created_at"`
	Updated  time.Time       `json:"updated_at"`
	Comments int             `json:"comments"`
	PR       json.RawMessage `json:"pull_request"`
}
type receipt struct {
	Next      string          `json:"next,omitempty"`
	Endpoint  string          `json:"endpoint"`
	Retrieved time.Time       `json:"retrieved_at"`
	SHA256    string          `json:"sha256"`
	Data      json.RawMessage `json:"data"`
}

func api(endpoint string) (receipt, error) {
	if strings.HasPrefix(endpoint, "https://") {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host != "api.github.com" {
			return receipt{}, fmt.Errorf("untrusted pagination host")
		}
	}
	out, err := exec.Command("gh", "api", "--include", "--method", "GET", endpoint).Output()
	if err != nil {
		return receipt{}, fmt.Errorf("read-only GitHub API %s: %w", endpoint, err)
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(out)), nil)
	if err != nil {
		return receipt{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return receipt{}, err
	}
	// Follow the cursor supplied by GitHub, including when page=100 is forbidden.
	next := ""
	match := regexp.MustCompile(`<([^>]+)>;\s*rel="next"`).FindStringSubmatch(response.Header.Get("Link"))
	if len(match) == 2 {
		next = match[1]
	}
	var raw any
	if err = json.Unmarshal(body, &raw); err != nil {
		return receipt{}, err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return receipt{}, err
	}
	sum := sha256.Sum256(data)
	return receipt{Next: next, Endpoint: endpoint, Retrieved: time.Now().UTC(), SHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}
func cached(path, endpoint string) (receipt, error) {
	if b, err := os.ReadFile(path); err == nil {
		var r receipt
		if err = json.Unmarshal(b, &r); err != nil {
			return receipt{}, err
		}
		sum := sha256.Sum256(r.Data)
		if r.Endpoint != endpoint || hex.EncodeToString(sum[:]) != r.SHA256 {
			return receipt{}, fmt.Errorf("invalid cached receipt %s", path)
		}
		return r, nil
	} else if !os.IsNotExist(err) {
		return receipt{}, err
	}
	r, err := api(endpoint)
	if err != nil {
		return receipt{}, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return receipt{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return receipt{}, err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return receipt{}, err
	}
	if err = f.Close(); err != nil {
		return receipt{}, err
	}
	return r, nil
}

type termMatcher struct {
	term       string
	expression *regexp.Regexp
}

func compileTerms(terms []string) []termMatcher {
	matchers := make([]termMatcher, 0, len(terms))
	for _, term := range terms {
		matchers = append(matchers, termMatcher{term, regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`)})
	}
	return matchers
}
func keywordHits(text string, matchers []termMatcher) []string {
	hits := []string{}
	for _, m := range matchers {
		if m.expression.MatchString(text) {
			hits = append(hits, m.term)
		}
	}
	return hits
}
func exportRepo(root, repo string, cfg config, cutoff time.Time) error {
	matchers := compileTerms(cfg.Terms)

	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repo) {
		return fmt.Errorf("invalid repository %q", repo)
	}
	dir := filepath.Join(root, strings.ReplaceAll(repo, "/", "__"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	meta, err := cached(filepath.Join(dir, "repository.json"), "repos/"+repo)
	if err != nil {
		return err
	}
	var identity struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	}
	if err = json.Unmarshal(meta.Data, &identity); err != nil {
		return err
	}
	records := make([]map[string]any, 0)
	seen := map[int64]bool{}
	matched, totalComments := 0, 0
	pages := 0
	endpoint := fmt.Sprintf("repos/%s/issues?state=all&sort=created&direction=asc&per_page=100", repo)
	endpoints := map[string]bool{}
	for page := 1; ; page++ {
		data, err := cached(filepath.Join(dir, fmt.Sprintf("cursor-page-%05d.json", page)), endpoint)
		if err != nil {
			return err
		}
		var items []issue
		if err = json.Unmarshal(data.Data, &items); err != nil {
			return err
		}
		pages++
		reachedCutoff := false
		for _, it := range items {
			if !it.Created.Before(cutoff) {
				reachedCutoff = true
				continue
			}
			if seen[it.ID] {
				return fmt.Errorf("duplicate issue ID %d across pages; inventory shifted", it.ID)
			}
			seen[it.ID] = true
			hits := keywordHits(it.Title+"\n"+it.Body, matchers)
			if len(hits) > 0 {
				matched++
			}
			totalComments += it.Comments
			stable := fmt.Sprintf("%d:%d", identity.ID, it.Number)
			holdout := sha256.Sum256([]byte(stable))
			records = append(records, map[string]any{"stable_id": stable, "repository_id": identity.ID, "repository": identity.FullName, "requested_repository": repo, "private": identity.Private, "issue_id": it.ID, "number": it.Number, "url": it.URL, "title": it.Title, "created_at": it.Created, "updated_at": it.Updated, "is_pr": len(it.PR) > 0, "keyword_hits": hits, "disposition": "unreviewed", "holdout_candidate": holdout[0] < 32, "comments_not_exported": it.Comments})
		}
		if page%25 == 0 {
			fmt.Printf("%s: %d pages, %d records captured\n", repo, page, len(records))
		}
		if reachedCutoff || data.Next == "" {
			break
		}
		if endpoints[data.Next] {
			return fmt.Errorf("pagination cursor cycle")
		}
		endpoints[data.Next] = true
		endpoint = data.Next
	}
	f, err := os.OpenFile(filepath.Join(dir, "inventory.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err = enc.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	summary := map[string]any{"repository": repo, "canonical_repository": identity.FullName, "repository_id": identity.ID, "private": identity.Private, "cutoff_exclusive": cfg.Cutoff, "completed_at": time.Now().UTC(), "pages": pages, "issue_pr_records": len(records), "title_body_keyword_candidates": matched, "comments_not_exported": totalComments, "classification": "unreviewed", "keyword_matching": "case-insensitive word boundaries", "terms": cfg.Terms, "coverage": "issue/PR title-body inventory only; comments, reviews, commits, linked fixes, release ancestry and manual classification remain open"}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "summary.json"), b, 0600); err != nil {
		return err
	}
	fmt.Printf("%s: inventory complete: %d records, %d keyword candidates; all dispositions unreviewed\n", repo, len(records), matched)
	return nil
}
func run() error {
	cfgPath := flag.String("config", "devel/formal/corpus-scope.json", "scope manifest")
	out := flag.String("out", "", "required private output directory outside repository")
	details := flag.String("details", "", "capture comments/reviews/fix metadata for a JSON entries manifest")
	only := flag.String("repo", "", "export only one repository from manifest (resume supported)")
	flag.Parse()
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	root, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	gitRoot, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	cwd := strings.TrimSpace(string(gitRoot))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(cwd, root)
	if err != nil {
		return err
	}
	if rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..") {
		return fmt.Errorf("private exports must be outside current repository directory")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return err
	}
	var cfg config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	cutoff, err := time.Parse(time.RFC3339, cfg.Cutoff)
	if err != nil {
		return err
	}
	if len(cfg.Repositories) == 0 || len(cfg.Terms) == 0 {
		return fmt.Errorf("empty scope")
	}
	if *details != "" {
		return exportDetails(root, *details, cfg)
	}
	matched := false
	for _, repo := range cfg.Repositories {
		if *only != "" && repo != *only {
			continue
		}
		matched = true
		if err = exportRepo(root, repo, cfg, cutoff); err != nil {
			return err
		}
	}
	if !matched {
		return fmt.Errorf("repository not in scope manifest")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

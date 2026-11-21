// Package gateway is the only way an agent touches a repository.
//
// Every rule here is enforced in code, never by prompt:
//   - paths are canonicalised and must stay inside the worktree; symlinks are
//     resolved and checked; .git is unreachable
//   - protected paths can never be written; out-of-scope writes are refused
//   - a file must be read before it can be edited (progressive disclosure)
//   - reads and searches are size-capped; the number of calls is capped
//   - repeating the same call three times stops the attempt (loop breaker)
//   - every call is audited; file contents are redacted before reaching the model
package gateway

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/bmatcuk/doublestar/v4"

	"aegis/internal/dlp"
)

// Call is one audited tool invocation.
type Call struct {
	Tool       string
	ArgsHash   string
	Allowed    bool
	Reason     string
	DurationMS int64
}

// Decision is one logged judgement call reported at finish.
type Decision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Outcome is the result of one tool call.
type Outcome struct {
	Content string
	IsError bool
	// Done ends the agent loop (finish or raise_conflict).
	Done bool
	// Abort ends the attempt because a limit was hit.
	Abort     bool
	Summary   string
	Decisions []Decision
	Conflict  *Conflict
}

// Conflict is a structured design conflict raised by the developer.
type Conflict struct {
	Reason      string   `json:"reason"`
	NeededPaths []string `json:"needed_paths"`
}

// Gateway holds the per-attempt state and limits.
type Gateway struct {
	Root        string // absolute worktree path
	Protected   []string
	Planned     []string
	Adjacent    []string
	Writable    bool // false for read-only roles (Architect)
	MaxRead     int
	MaxCalls    int
	MaxRunTests int
	// RunTests runs the repo's tests in the sandbox and returns a short summary.
	RunTests func(ctx context.Context, selector string) (string, error)
	// Audit records every call (ledger).
	Audit func(ctx context.Context, c Call)

	read       map[string]bool
	seen       map[string]int
	calls      int
	runs       int
	Redactions int
}

var (
	ErrOutside   = errors.New("path is outside the repository")
	ErrProtected = errors.New("protected path")
	selectorRe   = regexp.MustCompile(`^[A-Za-z0-9_./:\[\]-]*$`)
)

func (g *Gateway) init() {
	if g.read == nil {
		g.read = map[string]bool{}
		g.seen = map[string]int{}
	}
}

func match(globs []string, p string) bool {
	for _, gl := range globs {
		if ok, _ := doublestar.Match(gl, p); ok {
			return true
		}
	}
	return false
}

// clean validates a repo-relative path and returns its slash form.
func clean(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) {
		return "", ErrOutside
	}
	c := filepath.ToSlash(filepath.Clean(rel))
	if c == "." {
		return ".", nil
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", ErrOutside
	}
	for _, part := range strings.Split(c, "/") {
		if part == ".git" {
			return "", ErrOutside
		}
	}
	return c, nil
}

// within resolves symlinks and checks the result is inside the root.
func (g *Gateway) within(abs string) error {
	root, err := filepath.EvalSymlinks(g.Root)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return ErrOutside
	}
	return nil
}

func (g *Gateway) resolveExisting(rel string) (string, string, error) {
	c, err := clean(rel)
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(g.Root, filepath.FromSlash(c))
	if err := g.within(abs); err != nil {
		return "", "", err
	}
	return c, abs, nil
}

// canWrite applies the scope tiers to a write.
func (g *Gateway) canWrite(rel string) error {
	if !g.Writable {
		return errors.New("this role is read-only")
	}
	if match(g.Protected, rel) {
		return fmt.Errorf("%w: %s may never be modified by an agent", ErrProtected, rel)
	}
	if !match(g.Planned, rel) && !match(g.Adjacent, rel) {
		return fmt.Errorf("%s is outside the planned scope; if the change truly needs it, call raise_conflict", rel)
	}
	return nil
}

func argsHash(name string, input json.RawMessage) string {
	var v any
	_ = json.Unmarshal(input, &v)
	norm, _ := json.Marshal(v) // canonical key order
	sum := sha256.Sum256(append([]byte(name+"\x00"), norm...))
	return hex.EncodeToString(sum[:12])
}

func (g *Gateway) redact(s string) string {
	out, f := dlp.Redact(s)
	g.Redactions += f.Total()
	return out
}

// Execute runs one tool call with every check applied.
func (g *Gateway) Execute(ctx context.Context, name string, input json.RawMessage) Outcome {
	g.init()
	start := time.Now()
	hash := argsHash(name, input)
	out := g.execute(ctx, name, input, hash)
	if g.Audit != nil {
		reason := ""
		if out.IsError {
			reason = firstLine(out.Content)
		}
		g.Audit(ctx, Call{Tool: name, ArgsHash: hash, Allowed: !out.IsError, Reason: reason,
			DurationMS: time.Since(start).Milliseconds()})
	}
	return out
}

func fail(msg string) Outcome { return Outcome{Content: msg, IsError: true} }

func (g *Gateway) execute(ctx context.Context, name string, input json.RawMessage, hash string) Outcome {
	g.calls++
	if g.MaxCalls > 0 && g.calls > g.MaxCalls {
		return Outcome{Content: fmt.Sprintf("tool call limit (%d) reached for this attempt", g.MaxCalls), IsError: true, Abort: true}
	}
	g.seen[hash]++
	if g.seen[hash] >= 3 && name != "run_tests" {
		return Outcome{Content: "the same call was repeated three times; stopping this attempt", IsError: true, Abort: true}
	}
	switch name {
	case "list_files":
		var in struct {
			Dir string `json:"dir"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return fail("invalid input: " + err.Error())
		}
		return g.listFiles(in.Dir)
	case "read_file":
		var in struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return fail("invalid input: " + err.Error())
		}
		return g.readFile(in.Path, in.StartLine, in.EndLine)
	case "search_code":
		var in struct {
			Pattern  string `json:"pattern"`
			PathGlob string `json:"path_glob"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return fail("invalid input: " + err.Error())
		}
		return g.search(in.Pattern, in.PathGlob)
	case "edit_file":
		var in struct {
			Path    string `json:"path"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return fail("invalid input: " + err.Error())
		}
		return g.editFile(in.Path, in.OldText, in.NewText)
	case "create_file":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return fail("invalid input: " + err.Error())
		}
		return g.createFile(in.Path, in.Content)
	case "run_tests":
		var in struct {
			Selector string `json:"selector"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return fail("invalid input: " + err.Error())
		}
		return g.runTests(ctx, in.Selector)
	case "raise_conflict":
		var c Conflict
		if err := json.Unmarshal(input, &c); err != nil || c.Reason == "" {
			return fail("raise_conflict needs a reason")
		}
		return Outcome{Content: "conflict recorded", Done: true, Conflict: &c}
	case "finish":
		var in struct {
			Summary   string     `json:"summary"`
			Decisions []Decision `json:"decisions"`
		}
		if err := json.Unmarshal(input, &in); err != nil || in.Summary == "" {
			return fail("finish needs a summary")
		}
		return Outcome{Content: "done", Done: true, Summary: in.Summary, Decisions: in.Decisions}
	default:
		return fail("unknown tool " + name)
	}
}

func (g *Gateway) listFiles(dir string) Outcome {
	if dir == "" {
		dir = "."
	}
	c, abs, err := g.resolveExisting(dir)
	if err != nil {
		return fail(err.Error())
	}
	var files []string
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "__pycache__" || d.Name() == ".venv") {
			return filepath.SkipDir
		}
		if !d.IsDir() && len(files) < 500 {
			rel, _ := filepath.Rel(g.Root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		return Outcome{Content: "no files under " + c}
	}
	return Outcome{Content: strings.Join(files, "\n")}
}

func (g *Gateway) readFile(rel string, from, to int) Outcome {
	c, abs, err := g.resolveExisting(rel)
	if err != nil {
		return fail(err.Error())
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return fail("not a readable file: " + c)
	}
	f, err := os.Open(abs)
	if err != nil {
		return fail(err.Error())
	}
	defer f.Close()
	var b strings.Builder
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	n, truncated := 0, false
	for s.Scan() {
		n++
		if from > 0 && n < from {
			continue
		}
		if to > 0 && n > to {
			break
		}
		line := fmt.Sprintf("%5d  %s\n", n, s.Text())
		if g.MaxRead > 0 && b.Len()+len(line) > g.MaxRead {
			truncated = true
			break
		}
		b.WriteString(line)
	}
	g.read[c] = true
	out := g.redact(b.String())
	if truncated {
		out += fmt.Sprintf("\n[truncated at %d bytes; read further with start_line]", g.MaxRead)
	}
	return Outcome{Content: out}
}

func (g *Gateway) search(pattern, glob string) Outcome {
	if len(pattern) > 200 {
		return fail("pattern too long")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fail("invalid pattern: " + err.Error())
	}
	var hits []string
	_ = filepath.WalkDir(g.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(hits) >= 50 {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "__pycache__" || d.Name() == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(g.Root, p)
		rel = filepath.ToSlash(rel)
		if glob != "" && !match([]string{glob}, rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				if len(line) > 200 {
					line = line[:200]
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
				if len(hits) >= 50 {
					break
				}
			}
		}
		return nil
	})
	if len(hits) == 0 {
		return Outcome{Content: "no matches"}
	}
	return Outcome{Content: g.redact(strings.Join(hits, "\n"))}
}

func (g *Gateway) editFile(rel, oldText, newText string) Outcome {
	c, abs, err := g.resolveExisting(rel)
	if err != nil {
		return fail(err.Error())
	}
	if err := g.canWrite(c); err != nil {
		return fail(err.Error())
	}
	if !g.read[c] {
		return fail("read " + c + " before editing it")
	}
	if info, err := os.Lstat(abs); err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fail("can only edit regular files")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fail(err.Error())
	}
	if oldText == "" {
		return fail("old_text must not be empty")
	}
	if n := strings.Count(string(data), oldText); n != 1 {
		return fail(fmt.Sprintf("old_text must match exactly once in %s (matched %d times); include more surrounding lines", c, n))
	}
	updated := strings.Replace(string(data), oldText, newText, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return fail(err.Error())
	}
	return Outcome{Content: "edited " + c}
}

func (g *Gateway) createFile(rel, content string) Outcome {
	c, err := clean(rel)
	if err != nil || c == "." {
		return fail(ErrOutside.Error())
	}
	if err := g.canWrite(c); err != nil {
		return fail(err.Error())
	}
	abs := filepath.Join(g.Root, filepath.FromSlash(c))
	if _, err := os.Lstat(abs); err == nil {
		return fail(c + " already exists; use edit_file")
	}
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fail(err.Error())
	}
	if err := g.within(parent); err != nil {
		return fail(err.Error())
	}
	if len(content) > 256*1024 {
		return fail("file too large")
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fail(err.Error())
	}
	g.read[c] = true
	return Outcome{Content: "created " + c}
}

func (g *Gateway) runTests(ctx context.Context, selector string) Outcome {
	if !g.Writable || g.RunTests == nil {
		return fail("run_tests is not available to this role")
	}
	if !selectorRe.MatchString(selector) || strings.HasPrefix(selector, "-") {
		return fail("selector may only name test files or node ids")
	}
	g.runs++
	if g.MaxRunTests > 0 && g.runs > g.MaxRunTests {
		return fail(fmt.Sprintf("run_tests limit (%d) reached for this attempt", g.MaxRunTests))
	}
	summary, err := g.RunTests(ctx, selector)
	if err != nil {
		return fail("could not run tests: " + err.Error())
	}
	return Outcome{Content: g.redact(summary)}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ---- tool definitions for the model (strict schemas) ----

func tool(name, desc string, props map[string]any) anthropic.ToolUnionParam {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	sort.Strings(req)
	t := anthropic.ToolParam{
		Name:        name,
		Description: anthropic.String(desc),
		Strict:      anthropic.Bool(true),
		InputSchema: anthropic.ToolInputSchemaParam{Properties: props, Required: req,
			ExtraFields: map[string]any{"additionalProperties": false}},
	}
	return anthropic.ToolUnionParam{OfTool: &t}
}

var (
	str = map[string]any{"type": "string"}
	num = map[string]any{"type": "integer"}
)

// Tools returns the tool definitions this gateway allows, in a stable order
// (a stable tool list keeps the prompt cache warm).
func (g *Gateway) Tools() []anthropic.ToolUnionParam {
	tools := []anthropic.ToolUnionParam{
		tool("list_files", "List repository files under a directory (repo-relative; \".\" for the root).", map[string]any{"dir": str}),
		tool("read_file", "Read a repository file with line numbers. Use start_line/end_line (1-based, 0 = from start / to end) to page through large files.",
			map[string]any{"path": str, "start_line": num, "end_line": num}),
		tool("search_code", "Search repository files with a regular expression. path_glob narrows the files (e.g. \"app/**/*.py\"); empty searches everything.",
			map[string]any{"pattern": str, "path_glob": str}),
	}
	if g.Writable {
		tools = append(tools,
			tool("edit_file", "Replace old_text with new_text in a file you have already read. old_text must match exactly once.",
				map[string]any{"path": str, "old_text": str, "new_text": str}),
			tool("create_file", "Create a new file inside the planned scope.", map[string]any{"path": str, "content": str}),
			tool("run_tests", "Run the repository's tests in the sandbox. selector is a test file or node id; empty runs everything.",
				map[string]any{"selector": str}),
			tool("raise_conflict", "Stop and report that the plan cannot be implemented within its scope. List the extra paths you would need.",
				map[string]any{"reason": str, "needed_paths": map[string]any{"type": "array", "items": str}}),
			tool("finish", "Finish the attempt with a short summary and the judgement calls you made.",
				map[string]any{"summary": str, "decisions": map[string]any{"type": "array", "items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"decision", "reason"},
					"properties": map[string]any{"decision": str, "reason": str}}}}),
		)
	}
	return tools
}

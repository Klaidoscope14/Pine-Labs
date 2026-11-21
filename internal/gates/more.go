package gates

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Secrets runs gitleaks over the head content of every changed file.
// Any finding is a hard block. Reports are redacted so secrets never reach the ledger.
func Secrets(ctx context.Context, e Env) (Result, error) {
	out, err := e.outDir("secrets")
	if err != nil {
		return Result{}, err
	}
	scanDir := filepath.Join(out, "changed")
	n := 0
	for _, c := range e.Changes {
		if c.Status == "D" || c.Binary {
			continue
		}
		src := filepath.Join(e.HeadDir, filepath.FromSlash(c.Path))
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInfra, err)
		}
		dst := filepath.Join(scanDir, filepath.FromSlash(c.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInfra, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInfra, err)
		}
		n++
	}
	res := Result{Gate: "secrets", Metrics: map[string]float64{"files_scanned": float64(n)}}
	if n == 0 {
		res.Finalize()
		return res, nil
	}
	sres, err := e.run(ctx, e.HeadDir, out, []string{"gitleaks", "dir", "/scan", "--report-format", "json",
		"--report-path", "/out/gitleaks.json", "--exit-code", "0", "--no-banner", "--redact"},
		[]sandboxMount{{HostPath: scanDir, ContainerPath: "/scan", ReadOnly: true}}, nil)
	if err != nil {
		return Result{}, err
	}
	if sres.ExitCode != 0 {
		return Result{}, fmt.Errorf("gitleaks exited %d: %s", sres.ExitCode, tail(sres.Stderr, 400))
	}
	var report []struct {
		RuleID      string `json:"RuleID"`
		Description string `json:"Description"`
		File        string `json:"File"`
		StartLine   int    `json:"StartLine"`
	}
	if err := readReport(filepath.Join(out, "gitleaks.json"), e.MaxOutput, &report); err != nil {
		return Result{}, err
	}
	for _, r := range report {
		file := strings.TrimPrefix(filepath.ToSlash(r.File), "/scan/")
		res.Findings = append(res.Findings, Finding{RuleID: "FIN-002:" + r.RuleID, Severity: "CRITICAL", File: file,
			Line: r.StartLine, Message: "possible hard-coded secret: " + r.Description, Confidence: "HIGH",
			Verdict: HardBlock, Fingerprint: Fingerprint("secrets", r.RuleID, file, fmt.Sprint(r.StartLine))})
	}
	res.Finalize()
	return res, nil
}

var reqLine = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*(==|>=|<=|~=|!=|>|<)?\s*([^\s;#\\]*)`)

// packages extracts name -> version spec from requirements-style or pyproject content.
func packages(name string, data []byte) map[string]string {
	out := map[string]string{}
	s := bufio.NewScanner(bytes.NewReader(data))
	inDeps := false
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasSuffix(name, ".toml") {
			switch {
			case strings.HasPrefix(line, "dependencies") && strings.Contains(line, "["):
				inDeps = true
				line = line[strings.Index(line, "[")+1:]
			case inDeps && strings.HasPrefix(line, "]"):
				inDeps = false
				continue
			case !inDeps:
				continue
			}
			for _, part := range strings.Split(line, ",") {
				part = strings.Trim(strings.TrimSpace(part), `"'],`)
				if m := reqLine.FindStringSubmatch(part); m != nil && part != "" {
					out[strings.ToLower(m[1])] = m[2] + m[3]
				}
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if m := reqLine.FindStringSubmatch(line); m != nil {
			out[strings.ToLower(m[1])] = m[2] + m[3]
		}
	}
	return out
}

// Dependencies flags any new or changed dependency for human approval. Offline:
// it compares manifests between base and head instead of querying a CVE service.
func Dependencies(_ context.Context, e Env) (Result, error) {
	res := Result{Gate: "dependencies", Metrics: map[string]float64{}}
	changed := map[string]bool{}
	for _, c := range e.Changes {
		changed[c.Path] = true
	}
	added := 0
	for _, file := range e.Repo.DependencyFiles {
		if !changed[file] {
			continue
		}
		base, _ := os.ReadFile(filepath.Join(e.BaseDir, filepath.FromSlash(file)))
		head, _ := os.ReadFile(filepath.Join(e.HeadDir, filepath.FromSlash(file)))
		bp, hp := packages(file, base), packages(file, head)
		names := make([]string, 0, len(hp))
		for n := range hp {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			old, existed := bp[n]
			switch {
			case !existed:
				added++
				res.Findings = append(res.Findings, Finding{RuleID: "DEP-NEW", Severity: "HIGH", File: file,
					Message: fmt.Sprintf("new dependency %q requires human approval", n), Confidence: "HIGH",
					Verdict: Flag, Fingerprint: Fingerprint("dependencies", "new", file, n)})
			case old != hp[n]:
				res.Findings = append(res.Findings, Finding{RuleID: "DEP-CHANGED", Severity: "MEDIUM", File: file,
					Message: fmt.Sprintf("dependency %q changed from %q to %q", n, old, hp[n]), Confidence: "HIGH",
					Verdict: Flag, Fingerprint: Fingerprint("dependencies", "changed", file, n+old+hp[n])})
			}
		}
		if len(hp) == 0 && len(bp) == 0 {
			res.Findings = append(res.Findings, Finding{RuleID: "DEP-FILE-CHANGED", Severity: "MEDIUM", File: file,
				Message: "dependency file changed", Confidence: "MEDIUM", Verdict: Flag,
				Fingerprint: Fingerprint("dependencies", "file", file, "")})
		}
	}
	res.Metrics["new_dependencies"] = float64(added)
	res.Finalize()
	return res, nil
}

// OpenAPI exports the spec on base and head and fails on breaking changes the
// plan did not declare.
func OpenAPI(ctx context.Context, e Env) (Result, error) {
	out, err := e.outDir("openapi")
	if err != nil {
		return Result{}, err
	}
	res := Result{Gate: "openapi", Metrics: map[string]float64{}}
	export := func(tree, file string) (bool, string, error) {
		r, err := e.run(ctx, tree, "", e.Repo.OpenAPIExport, nil, map[string]string{"PYTHONPATH": "/work"})
		if err != nil {
			return false, "", err
		}
		if r.ExitCode != 0 || r.Truncated || !json.Valid(r.Stdout) {
			return false, tail(r.Stderr, 400), nil
		}
		spec, err := downgradeOpenAPI31(r.Stdout)
		if err != nil {
			return false, "spec is not valid JSON", nil
		}
		return true, "", os.WriteFile(filepath.Join(out, file), spec, 0o644)
	}
	if ok, why, err := export(e.BaseDir, "base.json"); err != nil {
		return Result{}, err
	} else if !ok {
		return Result{}, fmt.Errorf("%w: base OpenAPI export failed: %s", ErrInfra, why)
	}
	if ok, why, err := export(e.HeadDir, "head.json"); err != nil {
		return Result{}, err
	} else if !ok {
		res.Findings = append(res.Findings, Finding{RuleID: "OPENAPI-EXPORT", Severity: "HIGH",
			Message: "could not export the OpenAPI spec from the change: " + why, Confidence: "HIGH",
			Verdict: RemediableFail, Fingerprint: Fingerprint("openapi", "export", "", "")})
		res.Finalize()
		return res, nil
	}
	r, err := e.run(ctx, out, "", []string{"oasdiff", "breaking", "/work/base.json", "/work/head.json", "--format", "json"}, nil, nil)
	if err != nil {
		return Result{}, err
	}
	if r.ExitCode != 0 {
		return Result{}, fmt.Errorf("oasdiff exited %d: %s", r.ExitCode, tail(r.Stderr, 400))
	}
	var changes []struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		Level     int    `json:"level"`
		Operation string `json:"operation"`
		Path      string `json:"path"`
	}
	if trimmed := bytes.TrimSpace(r.Stdout); len(trimmed) > 0 && string(trimmed) != "null" {
		if err := json.Unmarshal(trimmed, &changes); err != nil {
			return Result{}, fmt.Errorf("oasdiff output: %w", err)
		}
	}
	for _, c := range changes {
		v, sev := Flag, "MEDIUM"
		if c.Level >= 3 {
			v, sev = HardBlock, "CRITICAL"
			if e.InterfaceChangesDeclared {
				v, sev = Flag, "HIGH"
			}
		}
		loc := strings.TrimSpace(c.Operation + " " + c.Path)
		res.Findings = append(res.Findings, Finding{RuleID: "OPENAPI-" + c.ID, Severity: sev, File: loc,
			Message: c.Text, Confidence: "HIGH", Verdict: v, Fingerprint: Fingerprint("openapi", c.ID, loc, c.Text)})
	}
	res.Metrics["breaking_changes"] = float64(len(changes))
	res.Finalize()
	return res, nil
}

// Replay feeds recorded webhook traffic through the changed service and checks
// ledger invariants. The harness and expectations live outside the repo.
func Replay(ctx context.Context, e Env) (Result, error) {
	out, err := e.outDir("replay")
	if err != nil {
		return Result{}, err
	}
	r, err := e.run(ctx, e.HeadDir, out, []string{"python", "/replay/replay.py", "/out/replay.json"},
		[]sandboxMount{{HostPath: e.ReplayDir, ContainerPath: "/replay", ReadOnly: true}},
		map[string]string{"PYTHONPATH": "/work"})
	if err != nil {
		return Result{}, err
	}
	res := Result{Gate: "replay", Metrics: map[string]float64{}}
	var report struct {
		Invariants []struct {
			Name   string `json:"name"`
			Held   bool   `json:"held"`
			Detail string `json:"detail"`
		} `json:"invariants"`
	}
	rerr := readReport(filepath.Join(out, "replay.json"), e.MaxOutput, &report)
	switch {
	case r.TimedOut:
		res.Findings = append(res.Findings, Finding{RuleID: "REPLAY-TIMEOUT", Severity: "HIGH",
			Message: "replay exceeded the timeout", Verdict: RemediableFail, Confidence: "HIGH",
			Fingerprint: Fingerprint("replay", "timeout", "", "")})
	case rerr != nil:
		res.Findings = append(res.Findings, Finding{RuleID: "REPLAY-CRASH", Severity: "HIGH",
			Message: "replay could not run: " + tail(r.Stderr, 400), Verdict: RemediableFail, Confidence: "HIGH",
			Fingerprint: Fingerprint("replay", "crash", "", "")})
	default:
		failed := 0
		for _, inv := range report.Invariants {
			if !inv.Held {
				failed++
				res.Findings = append(res.Findings, Finding{RuleID: "REPLAY-" + inv.Name, Severity: "HIGH",
					Message: inv.Detail, Verdict: RemediableFail, Confidence: "HIGH",
					Fingerprint: Fingerprint("replay", inv.Name, "", "")})
			}
		}
		if (failed == 0) != (r.ExitCode == 0) || len(report.Invariants) == 0 {
			res.Findings = append(res.Findings, Finding{RuleID: "REPLAY-INTEGRITY", Severity: "CRITICAL",
				Message: fmt.Sprintf("replay exit code %d disagrees with its report", r.ExitCode), Verdict: HardBlock,
				Confidence: "HIGH", Fingerprint: Fingerprint("replay", "integrity", "", "")})
		}
		res.Metrics["invariants"] = float64(len(report.Invariants))
		res.Metrics["violated"] = float64(failed)
	}
	res.Finalize()
	return res, nil
}

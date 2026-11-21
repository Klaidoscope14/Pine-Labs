package gates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Lint runs ruff. New lint errors send the change back to the developer.
func Lint(ctx context.Context, e Env) (Result, error) {
	scan := func(tree, tag string) ([]Finding, error) {
		out, err := e.outDir("lint-" + tag)
		if err != nil {
			return nil, err
		}
		res, err := e.run(ctx, tree, out, []string{"ruff", "check", "--no-cache", "--exit-zero",
			"--output-format", "json", "--output-file", "/out/ruff.json", "."}, nil, nil)
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("ruff exited %d: %s", res.ExitCode, tail(res.Stderr, 400))
		}
		var report []struct {
			Code     *string `json:"code"`
			Message  string  `json:"message"`
			Filename string  `json:"filename"`
			Location struct {
				Row int `json:"row"`
			} `json:"location"`
			EndLocation struct {
				Row int `json:"row"`
			} `json:"end_location"`
		}
		if err := readReport(filepath.Join(out, "ruff.json"), e.MaxOutput, &report); err != nil {
			return nil, err
		}
		var fs []Finding
		for _, r := range report {
			code := "E999"
			if r.Code != nil {
				code = *r.Code
			}
			file := workRel(r.Filename)
			fs = append(fs, Finding{RuleID: "ruff:" + code, Severity: "MEDIUM", File: file, Line: r.Location.Row,
				Message: r.Message, Confidence: "HIGH", Verdict: RemediableFail,
				Fingerprint: Fingerprint("lint", code, file, snippet(tree, file, r.Location.Row, r.EndLocation.Row))})
		}
		return fs, nil
	}
	return diffAware(ctx, e, "lint", scan)
}

// semgrepRulesKey changes whenever any rule file changes, so cached base results stay valid.
func semgrepRulesKey(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")) {
			data, _ := os.ReadFile(p)
			b.WriteString(p)
			b.Write(data)
		}
		return nil
	})
	return Fingerprint("semgrep-rules", "", "", b.String())
}

// Semgrep runs the fintech rule pack (FIN-001, 003, 004, 006).
// Only high-confidence ERROR rules fail a change; heuristic rules only flag.
func Semgrep(ctx context.Context, e Env) (Result, error) {
	targets := []string{}
	for _, d := range e.Repo.SourceDirs {
		targets = append(targets, "/work/"+strings.Trim(d, "/"))
	}
	scan := func(tree, tag string) ([]Finding, error) {
		out, err := e.outDir("semgrep-" + tag)
		if err != nil {
			return nil, err
		}
		cmd := append([]string{"semgrep", "scan", "--config", "/rules", "--json", "--output", "/out/semgrep.json",
			"--metrics", "off", "--disable-version-check", "--quiet", "--no-git-ignore"}, targets...)
		res, err := e.run(ctx, tree, out, cmd, rulesMount(e.RulesDir), map[string]string{"SEMGREP_ENABLE_VERSION_CHECK": "0"})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 && res.ExitCode != 1 {
			return nil, fmt.Errorf("semgrep exited %d: %s", res.ExitCode, tail(res.Stderr, 600))
		}
		var report struct {
			Results []struct {
				CheckID string             `json:"check_id"`
				Path    string             `json:"path"`
				Start   struct{ Line int } `json:"start"`
				End     struct{ Line int } `json:"end"`
				Extra   struct {
					Message  string `json:"message"`
					Severity string `json:"severity"`
					Metadata struct {
						FinID      string `json:"fin_id"`
						Confidence string `json:"confidence"`
						Block      string `json:"block"`
					} `json:"metadata"`
				} `json:"extra"`
			} `json:"results"`
			Errors []struct {
				Level   string `json:"level"`
				Message string `json:"message"`
				Path    string `json:"path"`
			} `json:"errors"`
		}
		if err := readReport(filepath.Join(out, "semgrep.json"), e.MaxOutput, &report); err != nil {
			return nil, err
		}
		var fs []Finding
		for _, r := range report.Results {
			file := workRel(r.Path)
			rule := r.Extra.Metadata.FinID
			if rule == "" {
				rule = r.CheckID
			}
			conf := strings.ToUpper(r.Extra.Metadata.Confidence)
			v := Flag
			sev := "MEDIUM"
			switch {
			case r.Extra.Metadata.Block == "hard":
				v, sev = HardBlock, "CRITICAL"
			case r.Extra.Severity == "ERROR" && conf == "HIGH":
				v, sev = RemediableFail, "HIGH"
			}
			fs = append(fs, Finding{RuleID: rule, Severity: sev, File: file, Line: r.Start.Line,
				Message: r.Extra.Message, Confidence: conf, Verdict: v,
				Fingerprint: Fingerprint("semgrep", rule, file, snippet(tree, file, r.Start.Line, r.End.Line))})
		}
		for _, er := range report.Errors {
			if strings.EqualFold(er.Level, "error") {
				file := workRel(er.Path)
				fs = append(fs, Finding{RuleID: "semgrep:parse-error", Severity: "HIGH", File: file,
					Message: "file could not be analysed: " + firstLine(er.Message), Confidence: "HIGH",
					Verdict: RemediableFail, Fingerprint: Fingerprint("semgrep", "parse", file, er.Message)})
			}
		}
		return fs, nil
	}
	return diffAwareKey(ctx, e, "semgrep", semgrepRulesKey(e.RulesDir), scan)
}

// Bandit runs Python security lint over source directories.
func Bandit(ctx context.Context, e Env) (Result, error) {
	targets := []string{}
	for _, d := range e.Repo.SourceDirs {
		targets = append(targets, "/work/"+strings.Trim(d, "/"))
	}
	scan := func(tree, tag string) ([]Finding, error) {
		out, err := e.outDir("bandit-" + tag)
		if err != nil {
			return nil, err
		}
		cmd := append([]string{"bandit", "-r", "-f", "json", "-o", "/out/bandit.json", "-q"}, targets...)
		res, err := e.run(ctx, tree, out, cmd, nil, nil)
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 && res.ExitCode != 1 {
			return nil, fmt.Errorf("bandit exited %d: %s", res.ExitCode, tail(res.Stderr, 400))
		}
		var report struct {
			Results []struct {
				TestID     string `json:"test_id"`
				Filename   string `json:"filename"`
				Line       int    `json:"line_number"`
				LineRange  []int  `json:"line_range"`
				Severity   string `json:"issue_severity"`
				Confidence string `json:"issue_confidence"`
				Text       string `json:"issue_text"`
			} `json:"results"`
		}
		if err := readReport(filepath.Join(out, "bandit.json"), e.MaxOutput, &report); err != nil {
			return nil, err
		}
		var fs []Finding
		for _, r := range report.Results {
			file := workRel(r.Filename)
			v := Flag
			if r.Severity == "HIGH" && r.Confidence == "HIGH" {
				v = RemediableFail
			}
			end := r.Line
			if n := len(r.LineRange); n > 0 {
				end = r.LineRange[n-1]
			}
			fs = append(fs, Finding{RuleID: "bandit:" + r.TestID, Severity: r.Severity, File: file, Line: r.Line,
				Message: r.Text, Confidence: r.Confidence, Verdict: v,
				Fingerprint: Fingerprint("bandit", r.TestID, file, snippet(tree, file, r.Line, end))})
		}
		return fs, nil
	}
	return diffAware(ctx, e, "bandit", scan)
}

func rulesMount(dir string) []sandboxMount {
	return []sandboxMount{{HostPath: dir, ContainerPath: "/rules", ReadOnly: true}}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// diffAware scans head and base and marks findings that already existed on base,
// so pre-existing problems never block an unrelated change.
func diffAware(ctx context.Context, e Env, gate string, scan func(tree, tag string) ([]Finding, error)) (Result, error) {
	return diffAwareKey(ctx, e, gate, "", scan)
}

func diffAwareKey(ctx context.Context, e Env, gate, extraKey string, scan func(tree, tag string) ([]Finding, error)) (Result, error) {
	head, err := scan(e.HeadDir, "head")
	if err != nil {
		return Result{}, err
	}
	base, err := e.cached(gate+"-"+extraKey, func() ([]Finding, error) { return scan(e.BaseDir, "base") })
	if err != nil {
		return Result{}, err
	}
	res := Result{Gate: gate, Findings: MarkPreExisting(head, base), Metrics: map[string]float64{}}
	resolved := Resolved(head, base)
	res.Metrics["resolved"] = float64(len(resolved))
	res.Resolved = resolved
	res.Finalize()
	if len(resolved) > 0 {
		names := []string{}
		for _, f := range resolved {
			names = append(names, f.RuleID)
		}
		res.Summary += fmt.Sprintf("; %d resolved from base (%s)", len(resolved), strings.Join(names, ", "))
	}
	return res, nil
}

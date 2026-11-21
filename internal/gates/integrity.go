package gates

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Integrity is FIN-013 (test tampering) and FIN-014 (silencing scanners): the
// agent may add tests and code, but it may not make the checks themselves weaker.
// It compares base and head copies of changed files, so it needs no sandbox.
//
//	Hard block (a human must decide): an existing test file deleted, an existing
//	test function removed, a skip/xfail added to a test file, an existing test
//	function losing asserts, or any edit to linter/test/coverage/scanner/CI config.
//	Must fix: a new suppression comment (noqa, nosec, nosemgrep, type: ignore,
//	pragma: no cover, pylint: disable).
func Integrity(_ context.Context, e Env) (Result, error) {
	res := Result{Gate: "integrity", Metrics: map[string]float64{}}
	add := func(rule, sev, file string, line int, msg string, v Verdict) {
		res.Findings = append(res.Findings, Finding{RuleID: rule, Severity: sev, File: file, Line: line,
			Message: msg, Confidence: "HIGH", Verdict: v, Fingerprint: Fingerprint("integrity", rule, file, msg)})
	}
	read := func(dir, rel string) (string, bool) {
		if rel == "" {
			return "", false
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		return string(data), err == nil
	}
	checked := 0
	for _, c := range e.Changes {
		file := c.Path
		oldPath := c.OldPath
		if oldPath == "" {
			oldPath = c.Path
		}
		if c.Binary {
			continue
		}
		base, inBase := read(e.BaseDir, oldPath)
		head, inHead := read(e.HeadDir, file)
		if c.Status == "D" {
			inHead = false
		}
		checked++

		if IsToolConfig(file) || (c.OldPath != "" && IsToolConfig(c.OldPath)) {
			if !inBase || !inHead || toolConfigChanged(file, base, head) {
				add("FIN-014", "CRITICAL", file, 0,
					"FIN-014: linter, test, coverage, scanner or CI configuration changed; a human must approve check configuration changes", HardBlock)
			}
		}
		if IsTestFile(oldPath) && inBase {
			if !inHead {
				add("FIN-013", "CRITICAL", oldPath, 0, "FIN-013: an existing test file was deleted", HardBlock)
				continue
			}
			bt, ht := testFunctions(base), testFunctions(head)
			names := make([]string, 0, len(bt))
			for n := range bt {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				h, ok := ht[n]
				switch {
				case !ok:
					add("FIN-013", "CRITICAL", file, 0, fmt.Sprintf("FIN-013: existing test %s was removed", n), HardBlock)
				case h.asserts < bt[n].asserts:
					add("FIN-013", "CRITICAL", file, h.line,
						fmt.Sprintf("FIN-013: existing test %s lost assertions (%d → %d)", n, bt[n].asserts, h.asserts), HardBlock)
				}
			}
		}
		if IsTestFile(file) && inHead && count(skipRe, head) > count(skipRe, base) {
			add("FIN-013", "CRITICAL", file, firstNewMatch(skipRe, head, base),
				"FIN-013: a skip or xfail was added to a test file", HardBlock)
		}
		if inHead && strings.HasSuffix(file, ".py") && count(suppressRe, head) > count(suppressRe, base) {
			add("FIN-014", "HIGH", file, firstNewMatch(suppressRe, head, base),
				"FIN-014: new suppression comment (noqa/nosec/nosemgrep/type: ignore/pragma: no cover/pylint: disable); fix the finding instead", RemediableFail)
		}
	}
	res.Metrics["files_checked"] = float64(checked)
	res.Finalize()
	return res, nil
}

var (
	skipRe     = regexp.MustCompile(`(?m)(pytest\.mark\.(skip|skipif|xfail)\b|pytest\.(skip|xfail)\(|unittest\.skip|@skip\b|\.skipTest\()`)
	suppressRe = regexp.MustCompile(`(?im)#\s*(noqa\b|nosec\b|nosemgrep\b|type:\s*ignore\b|pragma:\s*no\s*cover\b|pylint:\s*disable)`)
	defRe      = regexp.MustCompile(`^(\s*)(async\s+)?def\s+(test\w*)\s*\(`)
	classRe    = regexp.MustCompile(`^(\s*)class\s+(Test\w*)`)
	assertRe   = regexp.MustCompile(`^\s*(assert\b|self\.assert\w*\(|with\s+pytest\.raises\b|pytest\.raises\()`)
)

// IsTestFile reports whether a path is a test module.
func IsTestFile(p string) bool {
	base := path.Base(p)
	if !strings.HasSuffix(base, ".py") {
		return false
	}
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || base == "conftest.py" ||
		strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/")
}

// IsToolConfig reports files that configure the checks themselves.
func IsToolConfig(p string) bool {
	base := path.Base(p)
	switch base {
	case "ruff.toml", ".ruff.toml", "setup.cfg", "tox.ini", "pytest.ini", ".coveragerc", ".semgrepignore",
		".bandit", ".gitleaks.toml", ".gitleaksignore", ".pre-commit-config.yaml", "mypy.ini", ".flake8",
		"pyproject.toml":
		return true
	}
	return strings.HasPrefix(p, ".github/") || strings.HasPrefix(p, ".gitlab-ci") || strings.HasPrefix(p, ".circleci/")
}

// toolConfigChanged: for pyproject.toml only the [tool.*] check sections count
// (dependency edits are the dependency gate's job); any edit to other config counts.
func toolConfigChanged(file, base, head string) bool {
	if path.Base(file) != "pyproject.toml" {
		return base != head
	}
	return toolSections(base) != toolSections(head)
}

var toolSectionRe = regexp.MustCompile(`^\[tool\.(ruff|pytest|coverage|bandit|mypy|semgrep|pylint)`)

func toolSections(s string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			in = toolSectionRe.MatchString(t)
		}
		if in {
			b.WriteString(t + "\n")
		}
	}
	return b.String()
}

type testFn struct {
	line    int
	asserts int
}

// testFunctions maps "Class.test_name" or "test_name" to its assertion count.
func testFunctions(src string) map[string]testFn {
	out := map[string]testFn{}
	lines := strings.Split(src, "\n")
	class, classIndent := "", -1
	cur, curIndent := "", -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if cur != "" && indent <= curIndent {
			cur = ""
		}
		if class != "" && indent <= classIndent && !strings.HasPrefix(strings.TrimSpace(line), "@") {
			class = ""
		}
		if m := classRe.FindStringSubmatch(line); m != nil {
			class, classIndent = m[2], len(m[1])
			continue
		}
		if m := defRe.FindStringSubmatch(line); m != nil {
			name := m[3]
			if class != "" && len(m[1]) > classIndent {
				name = class + "." + name
			}
			cur, curIndent = name, len(m[1])
			out[name] = testFn{line: i + 1}
			continue
		}
		if cur != "" && assertRe.MatchString(line) {
			f := out[cur]
			f.asserts++
			out[cur] = f
		}
	}
	return out
}

func count(re *regexp.Regexp, s string) int { return len(re.FindAllStringIndex(s, -1)) }

// firstNewMatch returns the first line in head matching re that base does not contain.
func firstNewMatch(re *regexp.Regexp, head, base string) int {
	have := map[string]int{}
	for _, l := range strings.Split(base, "\n") {
		if re.MatchString(l) {
			have[strings.TrimSpace(l)]++
		}
	}
	for i, l := range strings.Split(head, "\n") {
		if !re.MatchString(l) {
			continue
		}
		k := strings.TrimSpace(l)
		if have[k] > 0 {
			have[k]--
			continue
		}
		return i + 1
	}
	return 0
}

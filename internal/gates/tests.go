package gates

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// junit is the subset of a pytest JUnit XML report we use.
type junitCase struct {
	Classname string `xml:"classname,attr"`
	Name      string `xml:"name,attr"`
	Failure   *struct {
		Message string `xml:"message,attr"`
	} `xml:"failure"`
	Error *struct {
		Message string `xml:"message,attr"`
	} `xml:"error"`
	Skipped *struct{} `xml:"skipped"`
}

type junitSuite struct {
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitReport struct {
	Tests, Failures, Errors, Skipped int
	Cases                            []junitCase
}

func parseJUnit(path string, max int64) (junitReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return junitReport{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return junitReport{}, err
	}
	if int64(len(data)) > max {
		return junitReport{}, errors.New("junit report too large")
	}
	var root struct {
		XMLName xml.Name
		Suites  []junitSuite `xml:"testsuite"`
		junitSuite
	}
	if err := xml.Unmarshal(data, &root); err != nil {
		return junitReport{}, err
	}
	suites := root.Suites
	if root.XMLName.Local == "testsuite" {
		suites = []junitSuite{root.junitSuite}
	}
	var r junitReport
	for _, s := range suites {
		r.Tests += s.Tests
		r.Failures += s.Failures
		r.Errors += s.Errors
		r.Skipped += s.Skipped
		r.Cases = append(r.Cases, s.Cases...)
	}
	return r, nil
}

// pytestOutcome turns a pytest run into a Result, cross-checking the exit code
// against the report so a tampered or crashed run can never pass.
func pytestOutcome(gate string, exit int, timedOut bool, report junitReport, reportErr error, stderr []byte, hideDetails bool) Result {
	res := Result{Gate: gate, Metrics: map[string]float64{}}
	add := func(rule, sev, file, msg string, v Verdict) {
		res.Findings = append(res.Findings, Finding{RuleID: rule, Severity: sev, File: file, Message: msg,
			Verdict: v, Confidence: "HIGH", Fingerprint: Fingerprint(gate, rule, file, msg)})
	}
	switch {
	case timedOut:
		add("TESTS-TIMEOUT", "HIGH", "", "test run exceeded the sandbox timeout", RemediableFail)
	case reportErr != nil && exit == 0:
		add("TEST-INTEGRITY", "CRITICAL", "", "tests reported success but produced no readable report", HardBlock)
	case reportErr != nil:
		add("TESTS-CRASH", "HIGH", "", fmt.Sprintf("tests could not run (exit %d): %s", exit, tail(stderr, 400)), RemediableFail)
	default:
		bad := report.Failures + report.Errors
		res.Metrics["tests"] = float64(report.Tests)
		res.Metrics["failed"] = float64(bad)
		res.Metrics["skipped"] = float64(report.Skipped)
		switch {
		case report.Tests == 0 || exit == 5:
			add("TESTS-NONE", "HIGH", "", "no tests were collected", RemediableFail)
		case exit == 0 && bad > 0, exit == 1 && bad == 0:
			add("TEST-INTEGRITY", "CRITICAL", "", fmt.Sprintf("exit code %d disagrees with report (%d failed)", exit, bad), HardBlock)
		case exit != 0 && exit != 1:
			add("TESTS-CRASH", "HIGH", "", fmt.Sprintf("pytest exited %d", exit), RemediableFail)
		}
		for _, c := range report.Cases {
			var msg string
			switch {
			case c.Failure != nil:
				msg = c.Failure.Message
			case c.Error != nil:
				msg = c.Error.Message
			default:
				continue
			}
			id := strings.TrimPrefix(c.Classname+"::"+c.Name, "::")
			if hideDetails {
				msg = "hidden acceptance test failed"
			}
			if len(msg) > 300 {
				msg = msg[:300]
			}
			add("TEST-FAILED", "HIGH", id, msg, RemediableFail)
		}
	}
	res.Finalize()
	if res.Verdict == Pass {
		res.Summary = fmt.Sprintf("%d passed, %d skipped", report.Tests-report.Skipped, report.Skipped)
	}
	return res
}

// Tests runs the repo's own test suite with coverage.
func Tests(ctx context.Context, e Env) (Result, error) {
	out, err := e.outDir("tests")
	if err != nil {
		return Result{}, err
	}
	res, err := e.run(ctx, e.HeadDir, out, []string{"python", "-m", "pytest", "-p", "no:cacheprovider",
		"--junitxml=/out/junit.xml", "--cov", "--cov-report=json:/out/coverage.json"},
		nil, map[string]string{"COVERAGE_FILE": "/tmp/.coverage"})
	if err != nil {
		return Result{}, err
	}
	report, rerr := parseJUnit(filepath.Join(out, "junit.xml"), e.MaxOutput)
	r := pytestOutcome("tests", res.ExitCode, res.TimedOut, report, rerr, res.Stderr, false)
	var cov struct {
		Totals struct {
			Percent float64 `json:"percent_covered"`
		} `json:"totals"`
	}
	if readReport(filepath.Join(out, "coverage.json"), e.MaxOutput, &cov) == nil {
		r.Metrics["coverage_percent"] = cov.Totals.Percent
	}
	return r, nil
}

// HiddenTests runs the hidden oracle suite. It uses its own conftest and config,
// never the repo's, so the agent cannot influence how it runs. Failure messages
// are withheld so the oracle isn't leaked back to the developer.
func HiddenTests(ctx context.Context, e Env) (Result, error) {
	out, err := e.outDir("hidden_tests")
	if err != nil {
		return Result{}, err
	}
	res, err := e.run(ctx, e.HeadDir, out, []string{"python", "-m", "pytest", "/hidden", "-c", "/hidden/pytest.ini",
		"--rootdir=/hidden", "-p", "no:cacheprovider", "--junitxml=/out/hidden.xml"},
		[]sandboxMount{{HostPath: e.HiddenDir, ContainerPath: "/hidden", ReadOnly: true}},
		map[string]string{"PYTHONPATH": "/work"})
	if err != nil {
		return Result{}, err
	}
	report, rerr := parseJUnit(filepath.Join(out, "hidden.xml"), e.MaxOutput)
	r := pytestOutcome("hidden_tests", res.ExitCode, res.TimedOut, report, rerr, nil, true)
	if report.Tests == 0 && rerr == nil {
		// A missing oracle is a configuration fault, never a pass.
		r.Verdict = HardBlock
		r.Summary = "hidden test suite is empty"
	}
	return r, nil
}

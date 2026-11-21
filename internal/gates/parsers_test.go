package gates

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const junitXML = `<?xml version="1.0" encoding="utf-8"?><testsuites><testsuite name="pytest" errors="0" failures="1" skipped="1" tests="3">
<testcase classname="tests.test_webhooks" name="test_ok"/>
<testcase classname="tests.test_webhooks" name="test_bad"><failure message="assert 1 == 2">trace</failure></testcase>
<testcase classname="tests.test_webhooks" name="test_skip"><skipped/></testcase>
</testsuite></testsuites>`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseJUnit(t *testing.T) {
	r, err := parseJUnit(writeTemp(t, "j.xml", junitXML), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tests != 3 || r.Failures != 1 || r.Skipped != 1 || len(r.Cases) != 3 {
		t.Fatalf("parsed %+v", r)
	}
	if _, err := parseJUnit(writeTemp(t, "big.xml", junitXML), 10); err == nil {
		t.Error("oversized report accepted")
	}
}

func TestPytestOutcomeIntegrity(t *testing.T) {
	ok := junitReport{Tests: 3}
	failing := junitReport{Tests: 3, Failures: 1, Cases: []junitCase{{Name: "t", Failure: &struct {
		Message string `xml:"message,attr"`
	}{"boom"}}}}
	cases := map[string]struct {
		exit    int
		timeout bool
		report  junitReport
		rerr    error
		want    Verdict
	}{
		"clean pass":                    {0, false, ok, nil, Pass},
		"real failure":                  {1, false, failing, nil, RemediableFail},
		"exit 0 but report has failure": {0, false, failing, nil, HardBlock},
		"exit 1 but report all green":   {1, false, ok, nil, HardBlock},
		"exit 0 with no report":         {0, false, junitReport{}, errors.New("missing"), HardBlock},
		"crash with no report":          {2, false, junitReport{}, errors.New("missing"), RemediableFail},
		"no tests collected":            {5, false, junitReport{}, nil, RemediableFail},
		"timeout":                       {-1, true, junitReport{}, nil, RemediableFail},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := pytestOutcome("tests", c.exit, c.timeout, c.report, c.rerr, nil, false)
			if r.Verdict != c.want {
				t.Errorf("verdict %s, want %s (%+v)", r.Verdict, c.want, r.Findings)
			}
		})
	}
}

func TestHiddenFailuresDoNotLeakDetails(t *testing.T) {
	failing := junitReport{Tests: 1, Failures: 1, Cases: []junitCase{{Name: "t", Failure: &struct {
		Message string `xml:"message,attr"`
	}{"assert balance == 50000"}}}}
	r := pytestOutcome("hidden_tests", 1, false, failing, nil, nil, true)
	for _, f := range r.Findings {
		if f.Message != "hidden acceptance test failed" {
			t.Errorf("hidden failure leaked: %q", f.Message)
		}
	}
}

func TestPackages(t *testing.T) {
	req := packages("requirements.txt", []byte("# c\nfastapi==0.115.4\nPydantic>=2\n-r other.txt\nuvicorn[standard]==0.32.0 ; python_version>'3'\n"))
	if req["fastapi"] != "==0.115.4" || req["pydantic"] != ">=2" || req["uvicorn"] != "==0.32.0" || len(req) != 3 {
		t.Errorf("requirements parsed as %v", req)
	}
	toml := packages("pyproject.toml", []byte("[project]\nname = \"x\"\ndependencies = [\n  \"httpx==0.27\",\n  \"rich\",\n]\n"))
	if toml["httpx"] != "==0.27" || toml["rich"] != "" || len(toml) != 2 {
		t.Errorf("pyproject parsed as %v", toml)
	}
}

func TestFindingStatusAcrossCommits(t *testing.T) {
	a := Finding{RuleID: "FIN-001", Fingerprint: Fingerprint("semgrep", "FIN-001", "a.py", "amount = float(x)")}
	b := Finding{RuleID: "FIN-004", Fingerprint: Fingerprint("semgrep", "FIN-004", "h.py", "store.credit(...)")}
	head := MarkPreExisting([]Finding{a, b}, []Finding{b})
	if head[0].PreExisting || !head[1].PreExisting {
		t.Errorf("pre-existing marking wrong: %+v", head)
	}
	if res := Resolved([]Finding{a}, []Finding{a, b}); len(res) != 1 || res[0].RuleID != "FIN-004" {
		t.Errorf("resolved = %+v", res)
	}
	// Line moves must not change identity.
	if Fingerprint("g", "r", "f", "x =  1\n") != Fingerprint("g", "r", "f", "x = 1") {
		t.Error("whitespace changed the fingerprint")
	}
}

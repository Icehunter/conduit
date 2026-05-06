package rtk

import (
	"strings"
	"testing"
)

// filterCase is a single classifier smoke test: verify the command is
// classified, the filter runs without panic, and (when wantSaved is true)
// the output is actually reduced.
type filterCase struct {
	name      string
	cmd       string
	output    string
	wantSaved bool
}

func runFilterCases(t *testing.T, cases []filterCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !IsClassified(tc.cmd) {
				t.Errorf("IsClassified(%q) = false; want true", tc.cmd)
			}
			r := Filter(tc.cmd, tc.output)
			if r.Filtered == "" && tc.output != "" {
				t.Errorf("Filter returned empty string for non-empty input")
			}
			if tc.wantSaved && r.SavedBytes <= 0 {
				t.Errorf("expected SavedBytes > 0 (savings); got %d\nfiltered:\n%s", r.SavedBytes, r.Filtered)
			}
		})
	}
}

func TestFilter_GH(t *testing.T) {
	longOutput := strings.Repeat("line of gh output that is very long and verbose\n", 200)
	runFilterCases(t, []filterCase{
		{"gh pr list", "gh pr list", longOutput, true},
		{"gh issue list", "gh issue list", longOutput, true},
		{"gh run list", "gh run list", longOutput, true},
	})
}

func TestFilter_GitLab(t *testing.T) {
	longOutput := strings.Repeat("line of glab output that is very long and verbose\n", 200)
	runFilterCases(t, []filterCase{
		{"glab mr list", "glab mr list", longOutput, true},
		{"glab ci status", "glab ci status", longOutput, true},
	})
}

func TestFilter_Files_Find(t *testing.T) {
	// filterFind truncates at 200 lines; use 500 to guarantee savings.
	longOutput := strings.Repeat("/very/long/file/path/that/is/nested/deeply/file.go\n", 500)
	runFilterCases(t, []filterCase{
		{"find", "find . -name '*.go'", longOutput, true},
		{"tree", "tree .", longOutput, true},
	})
}

func TestFilter_Files_Grep(t *testing.T) {
	// filterGrep truncates at 200 lines; use 500 to guarantee savings.
	longOutput := strings.Repeat("file.go:42: some matching line with lots of context here\n", 500)
	runFilterCases(t, []filterCase{
		{"rg", "rg 'pattern' .", longOutput, true},
		{"grep", "grep -r 'pattern' .", longOutput, true},
	})
}

func TestFilter_NPM(t *testing.T) {
	npmInstall := strings.Repeat("added package@1.0.0\n", 100) + "added 42 packages in 3.2s\n"
	npmTest := `
> my-app@1.0.0 test
> jest

PASS src/utils.test.ts
PASS src/api.test.ts

Test Suites: 2 passed, 2 total
Tests:       12 passed, 12 total
`
	runFilterCases(t, []filterCase{
		{"npm run build", "npm run build", npmInstall, false},
		{"npm run test", "npm run test", npmTest, false},
		{"pnpm run build", "pnpm run build", npmInstall, false},
		{"yarn test", "yarn test", npmTest, false},
		{"bun test", "bun test", npmTest, false},
	})
}

func TestFilter_Vitest(t *testing.T) {
	output := `
 ✓ src/utils.test.ts (12 tests) 45ms
 ✓ src/api.test.ts (8 tests) 23ms

 Test Files  2 passed (2)
      Tests  20 passed (20)
   Duration  1.23s
`
	runFilterCases(t, []filterCase{
		{"vitest", "vitest run", output, false},
		{"jest", "jest", output, false},
	})
}

func TestFilter_ESLint(t *testing.T) {
	output := `
/path/to/file.ts
  12:5  error  Unexpected any  @typescript-eslint/no-explicit-any
  45:1  warning  Missing return type  @typescript-eslint/explicit-module-boundary-types

✖ 1 error, 1 warning
`
	runFilterCases(t, []filterCase{
		{"eslint", "eslint src/", output, false},
		{"tsc", "tsc --noEmit", output, false},
	})
}

func TestFilter_Playwright(t *testing.T) {
	longOutput := strings.Repeat("  ✓  [chromium] › tests/login.spec.ts:10:5 › Login page loads (1.2s)\n", 100)
	longOutput += "\n  1 passed (5.2s)\n"
	runFilterCases(t, []filterCase{
		{"playwright", "npx playwright test", longOutput, true},
	})
}

func TestFilter_Cargo(t *testing.T) {
	cargoTest := `
   Compiling mylib v0.1.0
    Finished test [unoptimized + debuginfo] target(s) in 2.34s
     Running unittests src/lib.rs

running 5 tests
test a ... ok
test b ... ok
test c ... ok

test result: ok. 5 passed; 0 failed
`
	cargoBuild := strings.Repeat("   Compiling dep v1.0.0\n", 50) + "    Finished dev target\n"
	runFilterCases(t, []filterCase{
		{"cargo test", "cargo test", cargoTest, false},
		{"cargo build", "cargo build", cargoBuild, true},
		{"cargo clippy", "cargo clippy", "warning: unused variable `x`\n", false},
	})
}

func TestFilter_RuffMypy(t *testing.T) {
	ruffOut := "src/main.py:12:5: E501 line too long\nFound 1 error.\n"
	mypyOut := "src/main.py:10: error: Argument 1 to \"foo\" has incompatible type\nFound 1 error\n"
	runFilterCases(t, []filterCase{
		{"ruff check", "ruff check src/", ruffOut, false},
		{"mypy", "python3 -m mypy src/", mypyOut, false},
	})
}

func TestFilter_Ruby(t *testing.T) {
	rspecOut := `
Finished in 0.12345 seconds (files took 0.5 seconds to load)
5 examples, 0 failures
`
	rubocopOut := "Inspecting 10 files\n..........\n\n10 files inspected, no offenses detected\n"
	runFilterCases(t, []filterCase{
		{"rspec", "rspec spec/", rspecOut, false},
		{"rubocop", "rubocop app/", rubocopOut, false},
	})
}

func TestFilter_Dotnet(t *testing.T) {
	buildOut := `
Build started...
  Determining projects to restore...
  All projects are up-to-date for restore.
  MyApp -> bin/Debug/net8.0/MyApp.dll

Build succeeded.
    0 Warning(s)
    0 Error(s)
`
	testOut := `
Test run for MyApp.Tests.dll (.NETCoreApp,Version=v8.0)
Microsoft (R) Test Execution Command Line Tool Version 17.0.0

Passed!  - Failed:     0, Passed:     5, Skipped:     0, Total:     5
`
	runFilterCases(t, []filterCase{
		{"dotnet build", "dotnet build", buildOut, false},
		{"dotnet test", "dotnet test", testOut, false},
	})
}

func TestFilter_Docker(t *testing.T) {
	psOut := `CONTAINER ID   IMAGE     COMMAND   CREATED   STATUS    PORTS     NAMES
abc123def456   nginx     "nginx"   2 hours   Up 2h     80/tcp    web
`
	logsOut := strings.Repeat("2024-01-01 12:00:00 INFO request handled\n", 200)
	runFilterCases(t, []filterCase{
		{"docker ps", "docker ps", psOut, false},
		{"docker logs", "docker logs myapp", logsOut, true},
	})
}

func TestFilter_Kubectl(t *testing.T) {
	getOut := `NAME         READY   STATUS    RESTARTS   AGE
pod-abc123   1/1     Running   0          2d
pod-def456   1/1     Running   0          2d
`
	runFilterCases(t, []filterCase{
		{"kubectl get", "kubectl get pods", getOut, false},
		{"kubectl logs", "kubectl logs pod-abc123", strings.Repeat("log line\n", 200), true},
	})
}

func TestFilter_Terraform(t *testing.T) {
	planOut := `
Terraform used the selected providers to generate the following execution plan.

  # aws_instance.web will be created
  + resource "aws_instance" "web" {
      + ami           = "ami-0c55b159cbfafe1f0"
      + instance_type = "t2.micro"
    }

Plan: 1 to add, 0 to change, 0 to destroy.
`
	runFilterCases(t, []filterCase{
		{"terraform plan", "terraform plan", planOut, false},
		{"tofu plan", "tofu plan", planOut, false},
	})
}

func TestFilter_Make(t *testing.T) {
	makeOut := `cc -Wall -o main main.c utils.c
gcc -shared -o libfoo.so foo.c
make[1]: Entering directory '/project/sub'
make[1]: Leaving directory '/project/sub'
`
	runFilterCases(t, []filterCase{
		{"make", "make build", makeOut, false},
		{"make all", "make all", makeOut, false},
	})
}

func TestFilter_Maven(t *testing.T) {
	mvnOut := `[INFO] Scanning for projects...
[INFO] Building myapp 1.0.0
[INFO] Tests run: 5, Failures: 0, Errors: 0
[INFO] BUILD SUCCESS
[INFO] Total time: 3.456 s
`
	runFilterCases(t, []filterCase{
		{"mvn package", "mvn package", mvnOut, false},
		{"mvn test", "mvn install", mvnOut, false},
	})
}

func TestFilter_Swift(t *testing.T) {
	swiftOut := `Build complete!
Test Suite 'All tests' started at 2024-01-01 12:00:00.123
Test Suite 'MyTests' started
Test Case 'MyTests.testFoo' passed (0.001 seconds)
Test Suite 'MyTests' passed
`
	runFilterCases(t, []filterCase{
		{"swift build", "swift build", swiftOut, false},
		{"swift test", "swift test", swiftOut, false},
	})
}

func TestFilter_Curl(t *testing.T) {
	curlOut := `  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100  1024  100  1024    0     0  10240      0 --:--:-- --:--:-- --:--:-- 10240
{"status":"ok"}
`
	runFilterCases(t, []filterCase{
		{"curl", "curl https://api.example.com/health", curlOut, false},
	})
}

func TestFilter_Ping(t *testing.T) {
	pingOut := strings.Repeat("64 bytes from 8.8.8.8: icmp_seq=1 ttl=55 time=12.3 ms\n", 100)
	pingOut += "\n--- 8.8.8.8 ping statistics ---\n100 packets transmitted, 100 received\n"
	runFilterCases(t, []filterCase{
		{"ping", "ping 8.8.8.8", pingOut, true},
	})
}

func TestFilter_Shellcheck(t *testing.T) {
	scOut := `
In script.sh line 5:
  echo $var
       ^-- SC2086: Double quote to prevent globbing and word splitting.

For more information:
  https://www.shellcheck.net/wiki/SC2086
`
	runFilterCases(t, []filterCase{
		{"shellcheck", "shellcheck script.sh", scOut, false},
		{"yamllint", "yamllint config.yaml", "config.yaml:1:1: [warning] missing document start\n", false},
	})
}

func TestFilter_Golangci(t *testing.T) {
	lintOut := `
internal/foo/foo.go:12:5: exported function Foo should have comment or be unexported (golint)
internal/bar/bar.go:45:1: ineffectual assignment to err (ineffassign)

Issues: 2
`
	runFilterCases(t, []filterCase{
		{"golangci-lint", "golangci-lint run", lintOut, false},
	})
}

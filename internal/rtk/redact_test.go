package rtk

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	// Secrets are assembled from parts so the test file itself holds no
	// literal that a secret scanner would flag.
	awsKey := "AKIA" + "IOSFODNN7EXAMPLE"
	ghTok := "ghp_" + strings.Repeat("a1B2", 9)
	ghPAT := "github_pat_" + strings.Repeat("Ab3_", 8)
	antKey := "sk-ant-" + "api03-" + strings.Repeat("xY9", 10)
	oaiKey := "sk-" + "proj-" + strings.Repeat("Qw4", 12)
	slack := "xoxb-" + "1234567890-abcdefghij"
	stripe := "sk_live_" + strings.Repeat("a9", 10)
	gKey := "AIza" + strings.Repeat("Sy1", 12)[:35]
	jwt := "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0" + "." + "dBjftJeZ4CVPmB92K27uhbUJU1p1r"
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabcdef\n-----END RSA PRIVATE KEY-----"

	tests := []struct {
		name    string
		in      string
		want    string // exact expected output
		wantN   int
		notWant string // substring that must be gone
	}{
		{"aws key", "key=" + awsKey + " ok", "key=[REDACTED:aws-access-key] ok", 1, awsKey},
		{"github token", "token " + ghTok, "token [REDACTED:github-token]", 1, ghTok},
		{"github pat", "t: " + ghPAT, "t: [REDACTED:github-token]", 1, ghPAT},
		{"anthropic key", "x " + antKey + " y", "x [REDACTED:anthropic-key] y", 1, antKey},
		{"openai key", "OPENAI " + oaiKey, "OPENAI [REDACTED:openai-key]", 1, oaiKey},
		{"slack token", slack, "[REDACTED:slack-token]", 1, slack},
		{"stripe key", stripe, "[REDACTED:stripe-key]", 1, stripe},
		{"google key", "k=" + gKey, "k=[REDACTED:google-api-key]", 1, gKey},
		{"jwt", "Authorization: Bearer " + jwt, "Authorization: Bearer [REDACTED:jwt]", 1, jwt},
		{"private key block", "before\n" + pem + "\nafter", "before\n[REDACTED:private-key]\nafter", 1, "MIIEowIBAAKCAQEA"},
		{"truncated private key keeps body masked", "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0B\nAQEFAASCBKcwggSjAgEAAoIB\n", "[REDACTED:private-key]\n", 1, "MIIEvQIBADANBgkq"},
		{"private key with CRLF and short last line", "-----BEGIN RSA PRIVATE KEY-----\r\nMIIEowIBAAKCAQEAabcdefgh\r\nab==\r\n-----END RSA PRIVATE KEY-----\r\nafter", "[REDACTED:private-key]\r\nafter", 1, "MIIEowIBAAKCAQEA"},
		{"encrypted key headers", "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,ABCD\n\nMIIEowIBAAKCAQEAabcdefgh\n-----END RSA PRIVATE KEY-----", "[REDACTED:private-key]", 1, "DEK-Info"},
		{"header in code swallows nothing", "return bytes.HasPrefix(b, []byte(\"-----BEGIN OPENSSH PRIVATE KEY-----\"))\n}\n\nfunc other() int { return 42 }\n", "return bytes.HasPrefix(b, []byte(\"[REDACTED:private-key]\"))\n}\n\nfunc other() int { return 42 }\n", 1, ""},
		{"header then code lines", "-----BEGIN PRIVATE KEY-----\nreturn\nfunc main() {}\n", "[REDACTED:private-key]\nreturn\nfunc main() {}\n", 1, ""},
		{"pwd env", "PWD=/Users/x/src/proj\nOLDPWD=/Users/x/src\n", "PWD=/Users/x/src/proj\nOLDPWD=/Users/x/src\n", 0, ""},
		{"pwd with a mixed-class path", "PWD=/Users/Ryan.Wilson/Src2/Project-X\n", "PWD=/Users/Ryan.Wilson/Src2/Project-X\n", 0, ""},
		{"secret path value", "client_secret_file=/etc/secrets/db-credentials-v2.json\nsecret_ref = vault/path/to/x/y\n", "client_secret_file=/etc/secrets/db-credentials-v2.json\nsecret_ref = vault/path/to/x/y\n", 0, ""},
		{"go fixture", "cfg := Config{APIKey: \"test-key-1234\", Password: \"hunter2!!\"}", "cfg := Config{APIKey: \"test-key-1234\", Password: \"hunter2!!\"}", 0, ""},
		{"hex api key", "api_key=9f86d081884c7d659a2feaa0c55ad015", "api_key=[REDACTED:credential]", 1, "9f86d081"},
		{"hash is not a credential", "sha256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "sha256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", 0, ""},
		{"sk-learn identifier", "import sk-learn-classifier-module-for-the-pipeline-v2 and sk-something_long_identifier_name_here_1234", "import sk-learn-classifier-module-for-the-pipeline-v2 and sk-something_long_identifier_name_here_1234", 0, ""},
		{"legacy openai key", "k=sk-" + strings.Repeat("aB3dE", 10), "k=[REDACTED:openai-key]", 1, ""},
		{"url credentials", "git clone https://bob:hunter22pw@example.com/x.git", "git clone https://bob:[REDACTED:url-credentials]@example.com/x.git", 1, "hunter22pw"},
		{"url password already masked by a specific rule", "https://u:" + awsKey + "@h/", "https://u:[REDACTED:aws-access-key]@h/", 1, awsKey},
		{"password assignment", "DB_PASSWORD=s3cretValue99", "DB_PASSWORD=[REDACTED:credential]", 1, "s3cretValue99"},
		{"yaml quoted api key", `api_key: "abcDEF123456"`, `api_key: "[REDACTED:credential]"`, 1, "abcDEF123456"},
		{"json secret", `{"client_secret": "Zx81QpLm0Ab9"}`, `{"client_secret": "[REDACTED:credential]"}`, 1, "Zx81QpLm0Ab9"},
		{"two secrets", awsKey + " and " + ghTok, "[REDACTED:aws-access-key] and [REDACTED:github-token]", 2, awsKey},

		// Things that must survive.
		{"plain text", "hello world, nothing to see", "hello world, nothing to see", 0, ""},
		{"password identifier value", "password = hashedPassword", "password = hashedPassword", 0, ""},
		{"password type annotation", "secret: string", "secret: string", 0, ""},
		{"password call", "password=getPassword()", "password=getPassword()", 0, ""},
		{"password env ref", "password=${DB_PASS}", "password=${DB_PASS}", 0, ""},
		{"short value", "password=abc", "password=abc", 0, ""},
		{"sk in a word", "the task-" + strings.Repeat("a", 40) + " ran", "the task-" + strings.Repeat("a", 40) + " ran", 0, ""},
		{"short anthropic prefix", "sk-ant-short", "sk-ant-short", 0, ""},
		{"short sk dash", "sk-short", "sk-short", 0, ""},
		{"url without credentials", "https://example.com/a:b@c", "https://example.com/a:b@c", 0, ""},
		{"empty", "", "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := RedactSecrets(tt.in)
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
			if n != tt.wantN {
				t.Errorf("count = %d, want %d", n, tt.wantN)
			}
			if tt.notWant != "" && strings.Contains(got, tt.notWant) {
				t.Errorf("secret still present in %q", got)
			}
		})
	}
}

func TestRedactSecretsIdempotent(t *testing.T) {
	in := "a AKIA" + "IOSFODNN7EXAMPLE b password=Passw0rd!xyzAB https://u:pw123456@h/ c"
	once, _ := RedactSecrets(in)
	twice, n := RedactSecrets(once)
	if once != twice || n != 0 {
		t.Errorf("not idempotent: %q -> %q (n=%d)", once, twice, n)
	}
}

// FuzzRedactSecrets checks that arbitrary text never panics, that redaction is
// idempotent, and that a known secret embedded in it is always masked.
func FuzzRedactSecrets(f *testing.F) {
	for _, s := range []string{"", "password=abc12345", "-----BEGIN PRIVATE KEY-----", "https://a:b@c", "eyJ.eyJ.x", "sk-", "AKIA", "api_key: 'x'"} {
		f.Add(s)
	}
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	f.Fuzz(func(t *testing.T, s string) {
		out, _ := RedactSecrets(s)
		again, n := RedactSecrets(out)
		if again != out || n != 0 {
			t.Fatalf("not idempotent: %q -> %q", out, again)
		}
		// The embedded key needs a word boundary on each side to be a token.
		embedded, _ := RedactSecrets(s + "\n" + secret + "\n")
		if strings.Contains(embedded, secret) {
			t.Fatalf("embedded key survived: %q", embedded)
		}
	})
}

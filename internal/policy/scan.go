// Package policy scans app source for hardcoded secrets and unsupported
// external services before deployment. Secrets are blocking findings;
// external services are advisory warnings.
package policy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is a single policy hit.
type Finding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Excerpt string `json:"excerpt"`
}

// Result separates blocking secrets from advisory external-service findings.
type Result struct {
	Secrets  []Finding // hardcoded credentials — block deploy
	External []Finding // unsupported services — warn only
}

const maxScanBytes = 1 << 20 // 1 MiB: skip larger files (likely data/binaries)

// skipDirs are never scanned (build artifacts, vendored deps, VCS).
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "__pycache__": true,
	".venv": true, "venv": true, ".next": true, "dist": true,
	"build": true, "vendor": true, "coverage": true, ".vd": true,
}

// skipFiles are lockfiles and other low-signal, high-noise files.
var skipFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "go.sum": true,
}

// binaryExts are skipped wholesale.
var binaryExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".ico": true, ".svg": true, ".woff": true, ".woff2": true, ".ttf": true,
	".eot": true, ".otf": true, ".pdf": true, ".zip": true, ".gz": true,
	".tar": true, ".mp4": true, ".mp3": true, ".wav": true, ".mov": true,
	".wasm": true, ".bin": true, ".so": true, ".dylib": true, ".exe": true,
}

// secretPatterns are high-confidence, unambiguous credential formats.
// A match blocks the deploy.
var secretPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"private-key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9]{32,}`)},
	{"github-token", regexp.MustCompile(`\b(?:ghp|gho|ghs|ghu|ghr)_[0-9A-Za-z]{36}\b|\bgithub_pat_[0-9A-Za-z_]{50,}`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}`)},
	{"stripe-key", regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{24,}`)},
}

// dbURLPattern matches connection strings with an embedded password.
// Local/placeholder strings are filtered out in scanLine.
var dbURLPattern = regexp.MustCompile(`\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?)://([^:\s/@]+):([^@\s/]+)@([^\s/:"']+)`)

// jwtPattern is a suspected token — warns rather than blocks, because
// Supabase anon/publishable keys are JWTs and are meant to be public.
var jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)

// hostPattern flags unsupported managed-service hosts in source.
var hostPattern = regexp.MustCompile(`[A-Za-z0-9-]+\.(?:supabase\.co|firebaseio\.com|firebaseapp\.com|upstash\.io|mongodb\.net|planetscale\.com|redislabs\.com)`)

// forbiddenDeps are packages for capabilities the platform provides natively
// (databases, caches, object storage). Presence is an advisory warning.
var forbiddenDeps = map[string]string{
	"@supabase/supabase-js": "supabase", "supabase": "supabase",
	"firebase": "firebase", "firebase-admin": "firebase",
	"mongodb": "mongodb", "mongoose": "mongodb",
	"@planetscale/database": "planetscale",
	"redis":                 "redis", "ioredis": "redis",
	"aws-sdk": "aws", "@aws-sdk/client-s3": "aws-s3",
	"boto3": "aws", "@upstash/redis": "redis",
}

var localHosts = map[string]bool{
	"localhost": true, "127.0.0.1": true, "db": true,
	"postgres": true, "database": true, "mysql": true, "mongo": true,
}

var placeholderVals = map[string]bool{
	"password": true, "postgres": true, "root": true, "changeme": true,
	"example": true, "secret": true, "your-password": true, "pass": true,
	"": true,
}

// Scan walks srcDir and returns blocking secrets and advisory external findings.
func Scan(srcDir string) (*Result, error) {
	res := &Result{}
	seen := map[string]bool{} // dedupe by file:line:kind

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries, don't abort the scan
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		base := info.Name()
		// Never scan env files — they are the sanctioned secret channel.
		if base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env") {
			return nil
		}
		if skipFiles[base] || binaryExts[strings.ToLower(filepath.Ext(base))] {
			return nil
		}
		if info.Size() > maxScanBytes {
			return nil
		}

		rel, _ := filepath.Rel(srcDir, path)
		scanFile(path, rel, res, seen)

		// Dependency manifests get a structured pass for forbidden packages.
		switch base {
		case "package.json":
			scanPackageJSON(path, rel, res)
		case "requirements.txt", "pyproject.toml", "Pipfile":
			scanPyDeps(path, rel, res)
		}
		return nil
	})
	return res, err
}

func scanFile(path, rel string, res *Result, seen map[string]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		// Cheap binary guard: a NUL byte means this isn't source text.
		if strings.IndexByte(line, 0) >= 0 {
			return
		}
		scanLine(rel, lineNo, line, res, seen)
	}
}

func scanLine(rel string, lineNo int, line string, res *Result, seen map[string]bool) {
	add := func(list *[]Finding, kind, match string) {
		key := rel + ":" + itoa(lineNo) + ":" + kind
		if seen[key] {
			return
		}
		seen[key] = true
		*list = append(*list, Finding{File: rel, Line: lineNo, Kind: kind, Excerpt: redact(match)})
	}

	for _, p := range secretPatterns {
		if m := p.re.FindString(line); m != "" {
			if strings.Contains(m, "EXAMPLE") {
				continue // AWS's documented placeholder key, etc.
			}
			add(&res.Secrets, p.kind, m)
		}
	}

	if m := dbURLPattern.FindStringSubmatch(line); m != nil {
		pass, host := m[2], strings.ToLower(m[3])
		if !placeholderVals[strings.ToLower(pass)] && !localHosts[host] {
			add(&res.Secrets, "db-url-with-credentials", m[0])
		}
	}

	if m := jwtPattern.FindString(line); m != "" {
		add(&res.External, "suspected-jwt-token", m)
	}
	if m := hostPattern.FindString(line); m != "" {
		add(&res.External, "external-service-host", m)
	}
}

func scanPackageJSON(path, rel string, res *Result) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for name := range deps {
			if svc, ok := forbiddenDeps[name]; ok {
				res.External = append(res.External, Finding{File: rel, Kind: "unsupported-dependency:" + svc, Excerpt: name})
			}
		}
	}
}

func scanPyDeps(path, rel string, res *Result) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	text := strings.ToLower(string(data))
	for dep, svc := range forbiddenDeps {
		// Python deps use the bare name; skip the npm-scoped ones.
		if strings.HasPrefix(dep, "@") {
			continue
		}
		if regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(dep) + `\b`).MatchString(text) {
			res.External = append(res.External, Finding{File: rel, Kind: "unsupported-dependency:" + svc, Excerpt: dep})
		}
	}
}

// redact keeps only the first 4 chars so the secret never lands in logs.
func redact(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + strings.Repeat("*", 6)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

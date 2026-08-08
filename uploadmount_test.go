package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Everything the site writes must land inside a mounted volume, and the way it
// does so is silent enough to break by accident.
//
// uploadRoot is the RELATIVE path "data". It resolves to /data — and therefore
// meets the `uploads:/data` mount — only because the runtime stage of the
// Dockerfile sets no WORKDIR. Adding one would move every upload into the
// container layer, where `up --build` discards it, with no error anywhere and
// database rows still pointing at the vanished files. That has already happened
// once on this site; these assertions are what make the coupling loud.

// reFinalStage isolates the last FROM block of the Dockerfile — the runtime
// image. Earlier stages may set WORKDIR freely; only the final one matters.
func finalStage(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Skipf("no Dockerfile here: %v", err)
	}
	text := string(b)
	i := strings.LastIndex(text, "\nFROM ")
	if i < 0 {
		t.Fatal("Dockerfile has no FROM")
	}
	return text[i:]
}

func TestUploadRootMatchesTheMount(t *testing.T) {
	// The path the code writes to, absolute the way the container sees it.
	if filepath.IsAbs(uploadRoot) {
		// An absolute uploadRoot needs no WORKDIR reasoning, but it must still
		// be the mount point.
		if uploadRoot != "/data" {
			t.Fatalf("uploadRoot = %q, but the compose mount is uploads:/data", uploadRoot)
		}
		return
	}
	if uploadRoot != "data" {
		t.Fatalf("uploadRoot = %q — update this test and the compose mount together", uploadRoot)
	}
	// Relative: the runtime stage must not set a WORKDIR, or "data" stops being
	// "/data".
	if w := regexp.MustCompile(`(?m)^\s*WORKDIR\s+(\S+)`).FindStringSubmatch(finalStage(t)); w != nil {
		t.Errorf("the runtime stage sets WORKDIR %s, so uploadRoot %q resolves to %s/%s "+
			"and no longer meets the uploads:/data mount — every upload would go to the "+
			"container layer and be discarded by the next build",
			w[1], uploadRoot, w[1], uploadRoot)
	}
}

// The compose file must actually carry the mount the code depends on.
func TestComposeMountsTheUploadVolume(t *testing.T) {
	b, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Skipf("no docker-compose.yml here: %v", err)
	}
	compose := string(b)
	if !strings.Contains(compose, "uploads:/data") {
		t.Error("docker-compose.yml no longer mounts uploads:/data — uploads and backups " +
			"would live in the container layer")
	}
	// A named volume, not a bind mount into the checkout.
	if !regexp.MustCompile(`(?m)^volumes:`).MatchString(compose) ||
		!regexp.MustCompile(`(?m)^\s{2}uploads:`).MatchString(compose) {
		t.Error("the uploads volume is not declared as a named volume")
	}
}

// Every credential main.go reads must be forwarded by compose, or setting it
// does nothing and the source silently registers as unconfigured — which is how
// TMDB cover art was unreachable for as long as the scraper has existed.
func TestEveryCredentialIsForwardedByCompose(t *testing.T) {
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Skipf("no docker-compose.yml here: %v", err)
	}
	// os.Getenv("X_API_KEY") / os.Getenv("X_CLIENT") — the credential shape.
	re := regexp.MustCompile(`os\.Getenv\("([A-Z0-9_]*(?:API_KEY|CLIENT|TOKEN|SECRET))"\)`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(main), -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if !strings.Contains(string(compose), name+":") {
			t.Errorf("main.go reads %s but docker-compose.yml does not forward it — "+
				"the variable is absent inside the container whatever the operator sets", name)
		}
	}
	if len(seen) == 0 {
		t.Error("found no credential reads in main.go — has the pattern changed?")
	}
}

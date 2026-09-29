package managedbin_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yogasw/wick/internal/agents/provider/managedbin"
	_ "github.com/yogasw/wick/internal/agents/provider/omp"
	_ "github.com/yogasw/wick/internal/agents/provider/opencode"
)

// TestE2EManagedBinaries downloads REAL omp and opencode releases from
// GitHub through the managed installer: previous + latest, verifies
// sha256 against the API digest, runs --version, checks an update to the
// same version is a no-op and that rollback needs no download.
//
// Gated: WICK_E2E_PROVIDER_BIN=1. WICK_E2E_PROVIDER_BIN_DIR picks the
// scratch root (default: a temp dir).
func TestE2EManagedBinaries(t *testing.T) {
	if os.Getenv("WICK_E2E_PROVIDER_BIN") != "1" {
		t.Skip("set WICK_E2E_PROVIDER_BIN=1 to download real provider binaries")
	}
	root := os.Getenv("WICK_E2E_PROVIDER_BIN_DIR")
	if root == "" {
		root = t.TempDir()
	}
	root = filepath.Join(root, "providers", "bin")
	m := managedbin.New()
	m.Root = func() string { return root }
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	t.Logf("host: %s", m.Host().Label())

	for _, typ := range []string{"omp", "opencode"} {
		t.Run(typ, func(t *testing.T) {
			latest, err := m.CheckLatest(ctx, typ, true)
			if err != nil {
				t.Fatal(err)
			}
			rels, err := m.Releases(ctx, typ)
			if err != nil {
				t.Fatal(err)
			}
			prev := ""
			for _, r := range rels {
				if !r.Prerelease && r.Tag != latest.Tag {
					prev = r.Tag
					break
				}
			}
			if prev == "" {
				t.Fatal("no previous release to roll back to")
			}
			for _, tag := range []string{prev, latest.Tag} {
				start := time.Now()
				j, err := m.Install(ctx, typ, tag)
				if err != nil {
					t.Fatalf("install %s: %v", tag, err)
				}
				st, _ := m.Status(typ)
				var vi managedbin.InstalledView
				for _, iv := range st.Installed {
					if iv.Version == j.Version {
						vi = iv
					}
				}
				t.Logf("%s %s: asset=%s asset_sha256=%s bin_sha256=%s --version=%q (%s)",
					typ, tag, vi.Asset, vi.AssetSHA256, vi.SHA256, vi.VersionOutput, time.Since(start).Round(time.Second))
				if st.Current != j.Version || !managedbin.MatchesTag(tag, j.Version) {
					t.Fatalf("current=%s job=%+v", st.Current, j)
				}
			}
			// same version again: no download
			j, err := m.Install(ctx, typ, latest.Tag)
			if err != nil || !strings.Contains(j.Message, "already installed") {
				t.Fatalf("no-op update: %v %+v", err, j)
			}
			t.Logf("%s update to %s again: %s", typ, latest.Tag, j.Message)
			// rollback: instant, sha256 re-checked, no download
			start := time.Now()
			if err := m.Activate(typ, managedbin.TagVersion(prev)); err != nil {
				t.Fatal(err)
			}
			raw, err := m.VerifyCurrent(ctx, typ)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s rollback to %s in %s, wick-run --version=%q", typ, prev, time.Since(start).Round(time.Millisecond), raw)
			// and forward again
			if err := m.Activate(typ, latest.Version); err != nil {
				t.Fatal(err)
			}
		})
	}
}

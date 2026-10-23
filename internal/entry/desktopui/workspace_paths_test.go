package desktopui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalWorkspacePathsKeepWorkbenchDirectoryIdentity(t *testing.T) {
	s := newTestServer(t, false)
	project := filepath.Join(s.booksDir, "canonical-project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.validWorkspaceLocked(canonical)
	if err != nil || got != project {
		t.Fatalf("canonical project rejected or changed browser identity: got=%q want=%q err=%v", got, project, err)
	}
	canonicalHome, err := filepath.EvalSymlinks(s.homeDir)
	if err != nil { // An unconfigured test server does not initialize a Host/store.
		if err := os.MkdirAll(s.homeDir, 0700); err != nil {
			t.Fatal(err)
		}
		canonicalHome, err = filepath.EvalSymlinks(s.homeDir)
	}
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.validWorkspaceLocked(canonicalHome)
	if err != nil || got != s.dir {
		t.Fatalf("canonical current project lost its active Host identity: %q (%v)", got, err)
	}
}

func TestWorkspacePhysicalParentAliasAcceptsOnlyDirectProjects(t *testing.T) {
	s := newTestServer(t, false)
	root := t.TempDir()
	physicalParent := filepath.Join(root, "physical-parent")
	if err := os.MkdirAll(filepath.Join(physicalParent, "books", "project", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "parent-alias")
	if err := os.Symlink(physicalParent, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	s.booksDir = filepath.Join(alias, "books")
	physicalProject := filepath.Join(physicalParent, "books", "project")
	got, err := s.validWorkspaceLocked(physicalProject)
	if err != nil || got != filepath.Join(s.booksDir, "project") {
		t.Fatalf("same physical project rejected or lost its configured root: %q (%v)", got, err)
	}
	for _, outside := range []string{filepath.Join(physicalProject, "nested"), physicalParent, filepath.Join(physicalParent, "books")} {
		if _, err := s.validWorkspaceLocked(outside); err == nil {
			t.Fatalf("physical-path normalization allowed a non-project directory: %s", outside)
		}
	}
	finalAlias := filepath.Join(s.booksDir, "linked-project")
	if err := os.Symlink(physicalProject, finalAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := s.validWorkspaceLocked(finalAlias); err == nil {
		t.Fatal("final-component symlink project was accepted")
	}
}

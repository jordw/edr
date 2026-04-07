package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// setupRefsRepo creates a temp repo with cross-file references for testing.
func setupRefsRepo(t *testing.T) (*OnDemand, string) {
	t.Helper()
	tmp := t.TempDir()
	// Resolve symlinks so paths match NormalizeRoot (macOS /var → /private/var)
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	os.MkdirAll(filepath.Join(tmp, "pkg"), 0755)

	os.WriteFile(filepath.Join(tmp, "pkg", "types.go"), []byte(`package pkg

type User struct {
	Name  string
	Email string
}

func NewUser(name, email string) *User {
	return &User{Name: name, Email: email}
}

func (u *User) String() string {
	return u.Name + " <" + u.Email + ">"
}
`), 0644)

	os.WriteFile(filepath.Join(tmp, "pkg", "service.go"), []byte(`package pkg

func CreateUser(name, email string) *User {
	u := NewUser(name, email)
	return u
}

func ListUsers() []*User {
	return nil
}

func FormatUser(u *User) string {
	return u.String()
}
`), 0644)

	os.WriteFile(filepath.Join(tmp, "pkg", "util.go"), []byte(`package pkg

func ValidateEmail(email string) bool {
	return len(email) > 0
}
`), 0644)

	db := NewOnDemand(tmp)
	t.Cleanup(func() { db.Close() })
	return db, tmp
}

func TestFindSameFileCallers(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()

	// NewUser is called by CreateUser (same file? no — different files)
	// String method is in types.go, called by FormatUser in service.go
	// Let's test with symbols in the same file: NewUser calls User (via &User{})
	callers, err := db.FindSameFileCallers(ctx, "User", filepath.Join(tmp, "pkg", "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	// NewUser and String both reference "User" in their bodies
	names := map[string]bool{}
	for _, c := range callers {
		names[c.Name] = true
	}
	if !names["NewUser"] {
		t.Error("NewUser should be a same-file caller of User (contains 'User' in body)")
	}
	if !names["String"] {
		t.Error("String method should be a same-file caller of User")
	}
}

func TestFindSemanticCallers(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()

	// FindSemanticCallers for NewUser — should find CreateUser in service.go
	callers, err := db.FindSemanticCallers(ctx, "NewUser", filepath.Join(tmp, "pkg", "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range callers {
		if c.Name == "CreateUser" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(callers))
		for i, c := range callers {
			names[i] = c.Name
		}
		t.Errorf("CreateUser should be a caller of NewUser; got callers: %v", names)
	}
}

func TestFindSemanticCallers_SkipsSelf(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()

	// FindSemanticCallers for NewUser should NOT include NewUser itself
	callers, err := db.FindSemanticCallers(ctx, "NewUser", filepath.Join(tmp, "pkg", "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range callers {
		if c.Name == "NewUser" {
			t.Error("FindSemanticCallers should skip the target symbol itself")
		}
	}
}

func TestFindReferencesInFile(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()

	// FindReferencesInFile for "User" — should find references across files
	refs, err := FindReferencesInFile(ctx, db, "User", filepath.Join(tmp, "pkg", "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		t.Fatal("expected references to User across files")
	}

	// Should include references from service.go
	serviceRefs := false
	for _, r := range refs {
		if filepath.Base(r.File) == "service.go" {
			serviceRefs = true
			break
		}
	}
	if !serviceRefs {
		files := map[string]bool{}
		for _, r := range refs {
			files[filepath.Base(r.File)] = true
		}
		t.Errorf("expected references from service.go; got files: %v", files)
	}
}

func TestFindReferencesInFile_NoFalsePositives(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()

	// ValidateEmail is only in util.go and not referenced elsewhere
	refs, err := FindReferencesInFile(ctx, db, "ValidateEmail", filepath.Join(tmp, "pkg", "util.go"))
	if err != nil {
		t.Fatal(err)
	}
	// Should find the definition itself but no cross-file refs
	for _, r := range refs {
		if filepath.Base(r.File) != "util.go" {
			t.Errorf("ValidateEmail should not have refs outside util.go, got ref in %s", filepath.Base(r.File))
		}
	}
}

func TestGetContainerAt(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()
	file := filepath.Join(tmp, "pkg", "types.go")

	// Line inside User struct (Name field is around line 4)
	container, err := db.GetContainerAt(ctx, file, 4)
	if err != nil {
		t.Fatalf("GetContainerAt line 4: %v", err)
	}
	if container.Name != "User" {
		t.Errorf("expected container User at line 4, got %s", container.Name)
	}
	if container.Type != "struct" {
		t.Errorf("expected struct, got %s", container.Type)
	}

	// Line outside any container (func NewUser is not a container)
	_, err = db.GetContainerAt(ctx, file, 8)
	if err == nil {
		t.Error("expected error for line 8 (inside function, not container)")
	}
}

func TestGetContainerAt_NestedSelection(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "pkg"), 0755)

	// File with a class containing a nested struct
	os.WriteFile(filepath.Join(tmp, "pkg", "nested.go"), []byte(`package pkg

type Outer struct {
	Inner struct {
		Value int
	}
	Name string
}
`), 0644)

	db := NewOnDemand(tmp)
	defer db.Close()
	ctx := context.Background()

	// Line inside Outer (Name field, line 7)
	container, err := db.GetContainerAt(ctx, filepath.Join(tmp, "pkg", "nested.go"), 7)
	if err != nil {
		t.Fatalf("GetContainerAt line 7: %v", err)
	}
	if container.Name != "Outer" {
		t.Errorf("expected Outer at line 7, got %s", container.Name)
	}
}

func TestFindDeps(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()

	// CreateUser calls NewUser — should be a dep
	sym, err := db.GetSymbol(ctx, filepath.Join(tmp, "pkg", "service.go"), "CreateUser")
	if err != nil {
		t.Fatal(err)
	}

	deps, err := FindDeps(ctx, db, sym)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, d := range deps {
		if d.Name == "NewUser" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(deps))
		for i, d := range deps {
			names[i] = d.Name
		}
		t.Errorf("NewUser should be a dep of CreateUser; got deps: %v", names)
	}
}

func TestFindDeps_SameFileFirst(t *testing.T) {
	db, tmp := setupRefsRepo(t)
	ctx := context.Background()

	// FormatUser calls u.String() — String is in types.go (cross-file)
	// But also references User which is in types.go
	sym, err := db.GetSymbol(ctx, filepath.Join(tmp, "pkg", "service.go"), "FormatUser")
	if err != nil {
		t.Fatal(err)
	}

	deps, err := FindDeps(ctx, db, sym)
	if err != nil {
		t.Fatal(err)
	}

	// Should have some deps (at least User or String from types.go)
	if len(deps) == 0 {
		t.Error("FormatUser should have at least one dep")
	}
}

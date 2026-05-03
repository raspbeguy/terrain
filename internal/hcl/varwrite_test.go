package hcl

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

func TestUpsertTfvar_FreshFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if err := UpsertTfvar(dir, "region", cty.StringVal("us-east-1")); err != nil {
		t.Fatalf("UpsertTfvar: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "terraform.tfvars"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := `region = "us-east-1"`
	if !strings.Contains(string(got), want) {
		t.Errorf("expected %q in output, got %q", want, got)
	}
}

func TestUpsertTfvar_ReplaceExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	initial := `region = "us-west-2"
` + `instance_count = 5
` + `# inline comment about size
` + `size = "t2.micro"
`
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace just `region`.
	if err := UpsertTfvar(dir, "region", cty.StringVal("eu-west-1")); err != nil {
		t.Fatalf("UpsertTfvar: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "terraform.tfvars"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, `"eu-west-1"`) {
		t.Errorf("new value missing: %q", s)
	}
	if strings.Contains(s, `"us-west-2"`) {
		t.Errorf("old value still present: %q", s)
	}
	// Other keys preserved.
	if !strings.Contains(s, "instance_count = 5") {
		t.Errorf("instance_count not preserved: %q", s)
	}
	if !strings.Contains(s, `"t2.micro"`) {
		t.Errorf("size not preserved: %q", s)
	}
	// Comment preserved.
	if !strings.Contains(s, "# inline comment about size") {
		t.Errorf("comment not preserved: %q", s)
	}
}

func TestUpsertTfvar_NumberAndBool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if err := UpsertTfvar(dir, "count", cty.NumberIntVal(42)); err != nil {
		t.Fatal(err)
	}
	if err := UpsertTfvar(dir, "enabled", cty.True); err != nil {
		t.Fatal(err)
	}
	if err := UpsertTfvar(dir, "secret_marker", cty.NullVal(cty.String)); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars"))
	s := string(got)
	// hclwrite right-aligns the `=` when multiple attributes sit at the
	// same level, so we can't assert exact spacing. Match key + value
	// independently instead.
	if !regexpMatch(s, `count\s*=\s*42`) {
		t.Errorf("count missing: %q", s)
	}
	if !regexpMatch(s, `enabled\s*=\s*true`) {
		t.Errorf("enabled missing: %q", s)
	}
	if !regexpMatch(s, `secret_marker\s*=\s*null`) {
		t.Errorf("null sentinel missing: %q", s)
	}
}

// regexpMatch is a tiny helper since we don't want to pull regexp into the
// non-test code. Used by the formatting-tolerant assertions above.
func regexpMatch(s, pattern string) bool {
	re := mustCompile(pattern)
	return re.MatchString(s)
}

func TestDeleteTfvar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	initial := `keep = "yes"
` + `remove = "old"
`
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := DeleteTfvar(dir, "remove"); err != nil {
		t.Fatalf("DeleteTfvar: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars"))
	s := string(got)
	if strings.Contains(s, "remove") {
		t.Errorf("remove key still present: %q", s)
	}
	if !strings.Contains(s, `keep = "yes"`) {
		t.Errorf("keep was nuked: %q", s)
	}
}

func TestDeleteTfvar_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// No file exists.
	if err := DeleteTfvar(dir, "anything"); err != nil {
		t.Errorf("DeleteTfvar on missing file should be no-op, got: %v", err)
	}
}

func TestDeleteTfvar_MissingKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	initial := `keep = "yes"`
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := DeleteTfvar(dir, "absent"); err != nil {
		t.Errorf("DeleteTfvar on missing key: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars"))
	if !strings.Contains(string(got), `keep = "yes"`) {
		t.Errorf("file was modified: %q", got)
	}
}

func TestUpsertTfvarExpr_HCLExpression(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// HCL expression — list literal — that we can't easily build via cty.
	if err := UpsertTfvarExpr(dir, "tags", `["frontend", "prod"]`); err != nil {
		t.Fatalf("UpsertTfvarExpr: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars"))
	s := string(got)
	if !strings.Contains(s, `["frontend", "prod"]`) {
		t.Errorf("expression not written verbatim: %q", s)
	}
}

func TestUpsertTfvar_CorruptInputReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Garbage that doesn't parse as HCL.
	corrupt := `this is not = valid hcl{{
`
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}

	err := UpsertTfvar(dir, "anything", cty.StringVal("x"))
	if err == nil {
		t.Fatal("expected parse error on corrupt input, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error message doesn't mention parse: %v", err)
	}
}

func TestUpsertTfvar_PreservesAttributeOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	initial := `# header
` + `first = "a"
` + `second = "b"
` + `third = "c"
`
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	// Update middle attribute.
	if err := UpsertTfvar(dir, "second", cty.StringVal("B")); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars"))
	s := string(got)

	// hclwrite preserves order; check that first appears before second appears before third.
	firstIdx := strings.Index(s, "first")
	secondIdx := strings.Index(s, "second")
	thirdIdx := strings.Index(s, "third")
	if firstIdx < 0 || secondIdx < 0 || thirdIdx < 0 {
		t.Fatalf("not all keys present: %q", s)
	}
	if !(firstIdx < secondIdx && secondIdx < thirdIdx) {
		t.Errorf("order disturbed: %q", s)
	}
	if !strings.Contains(s, `"B"`) {
		t.Errorf("new value missing: %q", s)
	}
}

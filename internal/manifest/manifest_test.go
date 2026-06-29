package manifest

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func parse(t *testing.T, body string) *Manifest {
	t.Helper()
	m, err := Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}

func TestParse_IgnoresCommentsAndBlanks(t *testing.T) {
	m := parse(t, "# a comment\n\n  \nfile.txt\n")
	got := m.Literals()
	want := []string{"file.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Literals() = %v, want %v", got, want)
	}
	if m.HasPatterns() {
		t.Errorf("HasPatterns() = true, want false for purely-literal manifest")
	}
}

func TestParse_LiteralVsPattern(t *testing.T) {
	m := parse(t, "pyrightconfig.json\ndocs/notes/\n*.env\n!keep.env\n")

	gotLit := append([]string(nil), m.Literals()...)
	sort.Strings(gotLit)
	wantLit := []string{"docs/notes/", "pyrightconfig.json"}
	if !reflect.DeepEqual(gotLit, wantLit) {
		t.Errorf("Literals() = %v, want %v", gotLit, wantLit)
	}
	if !m.HasPatterns() {
		t.Errorf("HasPatterns() = false, want true (wildcard + negation present)")
	}
}

func TestMatch_Literal(t *testing.T) {
	m := parse(t, "pyrightconfig.json\nconfig/local.toml\n")
	if !m.Match("pyrightconfig.json") {
		t.Error("pyrightconfig.json should be included")
	}
	if !m.Match("config/local.toml") {
		t.Error("config/local.toml should be included")
	}
	if m.Match("other.json") {
		t.Error("other.json should NOT be included")
	}
}

func TestMatch_WildcardAndNegation(t *testing.T) {
	m := parse(t, "*.env\n!secret.env\n")
	if !m.Match("local.env") {
		t.Error("local.env should be included by *.env")
	}
	if m.Match("secret.env") {
		t.Error("secret.env should be re-excluded by !secret.env")
	}
	if m.Match("notes.txt") {
		t.Error("notes.txt should not match")
	}
}

func TestMatch_DoubleStar(t *testing.T) {
	m := parse(t, "**/local.config\n")
	if !m.Match("a/b/local.config") {
		t.Error("a/b/local.config should match **/local.config")
	}
}

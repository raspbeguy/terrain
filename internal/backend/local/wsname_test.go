package local

import "testing"

func TestIsValidWorkspaceName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want bool
	}{
		{"default", true},
		{"staging", true},
		{"prod_2024", true},
		{"a-b-c", true},
		{"A1", true},
		{"", false},
		{"with space", false},
		{"with/slash", false},
		{"dot.s", false},
		{"colon:s", false},
		{"emoji😀", false},
	}
	for _, c := range cases {
		if got := IsValidWorkspaceName(c.name); got != c.want {
			t.Errorf("IsValidWorkspaceName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

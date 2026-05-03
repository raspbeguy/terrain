package local

import "testing"

func TestFormatArgsForLog(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no -var",
			args: []string{"plan", "-json", "-input=false", "-out=foo.tfplan"},
			want: "plan -json -input=false -out=foo.tfplan",
		},
		{
			name: "single -var",
			args: []string{"plan", "-var", "db_password=hunter2"},
			want: "plan -var db_password=<redacted>",
		},
		{
			name: "multiple -var",
			args: []string{"plan", "-var", "key1=secret1", "-var", "key2=secret2"},
			want: "plan -var key1=<redacted> -var key2=<redacted>",
		},
		{
			name: "-var without equals",
			args: []string{"plan", "-var", "weirdvalue"},
			want: "plan -var <redacted>",
		},
		{
			name: "interleaved with other flags",
			args: []string{"plan", "-var", "k=v", "-target=foo", "-var-file=bar"},
			want: "plan -var k=<redacted> -target=foo -var-file=bar",
		},
		{
			name: "-var-file is not redacted (path, not value)",
			args: []string{"plan", "-var-file=/path/to/file.json"},
			want: "plan -var-file=/path/to/file.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatArgsForLog(tc.args)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

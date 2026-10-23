package access

import "testing"

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"sdc", "'sdc'"},
		{"", "''"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"$(rm -rf /)", `'$(rm -rf /)'`},
		{"`id`", "'`id`'"},
		{"a;b|c&d>e<f", "'a;b|c&d>e<f'"},
		{"\n", "'\n'"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildCommand(t *testing.T) {
	got := BuildCommand([]string{"ceph", "osd", "df", "-f", "json"})
	want := "'ceph' 'osd' 'df' '-f' 'json'"
	if got != want {
		t.Errorf("BuildCommand = %q, want %q", got, want)
	}
}

func TestBuildCommandEscapesEveryArg(t *testing.T) {
	got := BuildCommand([]string{"grep", "-F", "a b"})
	want := "'grep' '-F' 'a b'"
	if got != want {
		t.Errorf("BuildCommand = %q, want %q", got, want)
	}
}

func TestBuildCommandRejectsEmptyArgv(t *testing.T) {
	if _, err := BuildCommandChecked(nil); err == nil {
		t.Fatal("expected error for empty argv")
	}
}

func TestBuildCommandNeutralizesInjection(t *testing.T) {
	// 参数里的 shell 元字符必须被单引号包裹，不产生额外命令
	got := BuildCommand([]string{"cat", "x; rm -rf /tmp/y"})
	want := "'cat' 'x; rm -rf /tmp/y'"
	if got != want {
		t.Fatalf("BuildCommand = %q, want %q", got, want)
	}
}

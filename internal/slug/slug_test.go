package slug

import "testing"

func TestSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain lowercase", "hello world", "hello-world"},
		{"mixed case and punctuation", "Human Friendly Branch Names!", "human-friendly-branch-names"},
		{"unicode/emoji only", "🎉🚀✨", "untitled"},
		{"empty string", "", "untitled"},
		{"repeated whitespace and hyphens collapse", "foo   --  bar", "foo-bar"},
		{"leading and trailing punctuation trimmed", "--foo bar--", "foo-bar"},
		{
			"truncation at 40 chars trims trailing hyphen",
			"this is a very long title that will definitely need truncation",
			"this-is-a-very-long-title-that-will-defi",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Slug(c.in); got != c.want {
				t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestBranch(t *testing.T) {
	got := Branch("Human Friendly Branch Names", "f7b61c59-203b-4884-944c-4f240d625985")
	want := "ticket/human-friendly-branch-names-f7b61c59"
	if got != want {
		t.Errorf("Branch() = %q, want %q", got, want)
	}
}

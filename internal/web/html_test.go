package web

import "testing"

func TestHTMLHeadings(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<h1>Title</h1>", "# Title"},
		{"<h2>Sub</h2>", "## Sub"},
		{"<h3>Third</h3>", "### Third"},
		{"<h4>Fourth</h4>", "#### Fourth"},
		{"<h5>Fifth</h5>", "##### Fifth"},
		{"<h6>Sixth</h6>", "###### Sixth"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLParagraphsAndBlocks(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<p>Hello</p>", "Hello"},
		{"<p>a</p><p>b</p>", "a\n\nb"},
		{"<div>a</div><div>b</div>", "a\n\nb"},
		{"<section>a</section><article>b</article>", "a\n\nb"},
		{"a<br>b", "a\nb"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLLinks(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<a href=\"https://example.com\">text</a>", "[text](https://example.com)"},
		{"<p>see <a href=\"https://x.com/a\">x</a> now</p>", "see [x](https://x.com/a) now"},
		{"<a href=\"/relative\">rel</a>", "rel"},
		{"<a href=\"\">empty</a>", "empty"},
		{"<a>no href</a>", "no href"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLBoldItalicCode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<strong>bold</strong>", "**bold**"},
		{"<b>b</b>", "**b**"},
		{"<em>it</em>", "*it*"},
		{"<i>i</i>", "*i*"},
		{"<code>x := 1</code>", "`x := 1`"},
		{"<p>a <strong>b</strong> c</p>", "a **b** c"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLPreCodeBlock(t *testing.T) {
	in := "<pre>if (a) { run(); }</pre>"
	want := "```\nif (a) { run(); }\n```"
	if got := HTMLToMarkdown(in); got != want {
		t.Errorf("HTMLToMarkdown(%q) = %q, want %q", in, got, want)
	}
}

func TestHTMLList(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<ul><li>a</li><li>b</li></ul>", "- a\n- b"},
		{"<ol><li>a</li><li>b</li></ol>", "1. a\n1. b"},
		{"<ul><li>only</li></ul>", "- only"},
		{"<p>x</p><ul><li>a</li></ul><p>y</p>", "x\n\n- a\n\ny"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLBlockquote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<blockquote>quote</blockquote>", "> quote"},
		{"<blockquote>line1\nline2</blockquote>", "> line1\n> line2"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLImage(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<img alt=\"pic\" src=\"https://x/i.png\">", "![pic](https://x/i.png)"},
		{"<img src=\"https://x/i.png\">", "![](https://x/i.png)"},
		{"<img alt=\"no src\">", "no src"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLScriptStyleDropped(t *testing.T) {
	in := "<p>keep</p><script>var x = \"drop\";</script><style>p { color: red }</style><p>still</p>"
	want := "keep\n\nstill"
	if got := HTMLToMarkdown(in); got != want {
		t.Errorf("HTMLToMarkdown(%q) = %q, want %q", in, got, want)
	}
	// script 内带 "</script" 前缀的字符串不能提前截断。
	in2 := "<script>var s = \"</scriptx\";</script><p>ok</p>"
	if got := HTMLToMarkdown(in2); got != "ok" {
		t.Errorf("script with partial end tag: got %q, want ok", got)
	}
}

func TestHTMLEntities(t *testing.T) {
	in := `<p>a &amp; b &lt;c&gt; &quot;q&quot; &#39;x&#39; &copy;</p>`
	want := "a & b <c> \"q\" 'x' ©"
	if got := HTMLToMarkdown(in); got != want {
		t.Errorf("HTMLToMarkdown(%q) = %q, want %q", in, got, want)
	}
}

func TestHTMLUnknownTagsStripped(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<p>a <span class=\"x\">b</span> c</p>", "a b c"},
		{"<div><table><tr><td>cell</td></tr></table></div>", "cell"},
		{"<article><p>a</p><footer>f</footer></article>", "a\n\nf"},
	}
	for _, tc := range tests {
		if got := HTMLToMarkdown(tc.in); got != tc.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMLCommentsAndDoctype(t *testing.T) {
	in := "<!DOCTYPE html><!-- note --><p>a</p>"
	want := "a"
	if got := HTMLToMarkdown(in); got != want {
		t.Errorf("HTMLToMarkdown(%q) = %q, want %q", in, got, want)
	}
}

func TestHTMLBlankLinesCollapsed(t *testing.T) {
	in := "<p>a</p>\n\n\n\n<p>b</p>"
	want := "a\n\nb"
	if got := HTMLToMarkdown(in); got != want {
		t.Errorf("HTMLToMarkdown(%q) = %q, want %q", in, got, want)
	}
}

func TestHTMLLiteralLessThan(t *testing.T) {
	in := "<p>5 < 6 and 7 &lt; 8</p>"
	want := "5 < 6 and 7 < 8"
	if got := HTMLToMarkdown(in); got != want {
		t.Errorf("HTMLToMarkdown(%q) = %q, want %q", in, got, want)
	}
}

func TestHTMLMixedDocument(t *testing.T) {
	in := `<article>
  <h1>Go generics</h1>
  <p>See <a href="https://go.dev/doc">the docs</a> and <code>slices</code>.</p>
  <ul><li>fast</li><li>safe</li></ul>
  <blockquote>ship it</blockquote>
</article>`
	want := "# Go generics\n\nSee [the docs](https://go.dev/doc) and `slices`.\n\n- fast\n- safe\n\n> ship it"
	if got := HTMLToMarkdown(in); got != want {
		t.Errorf("mixed document:\n got: %q\nwant: %q", got, want)
	}
}

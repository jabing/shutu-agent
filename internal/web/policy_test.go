package web

import (
	"net/url"
	"testing"
)

// TestClassifyContentType 覆盖 classifyContentType：html / text / unsupported 三分。
func TestClassifyContentType(t *testing.T) {
	tests := []struct {
		ct   string
		want string
	}{
		// html
		{"text/html", "html"},
		{"text/html; charset=utf-8", "html"},
		{"TEXT/HTML", "html"},
		{"application/xhtml+xml", "html"},
		{"application/xhtml+xml; charset=utf-8", "html"},
		// text
		{"text/plain", "text"},
		{"text/plain; charset=us-ascii", "text"},
		{"text/markdown", "text"},
		{"application/json", "text"},
		{"application/json; charset=utf-8", "text"},
		{"application/xml", "text"},
		{"application/javascript", "text"},
		{"application/ld+json", "text"},
		{"application/atom+xml", "text"},
		// unsupported
		{"image/png", "unsupported"},
		{"application/pdf", "unsupported"},
		{"application/octet-stream", "unsupported"},
		{"audio/mpeg", "unsupported"},
		// absent / malformed
		{"", "unsupported"},
		{"garbage; no equals", "unsupported"},
	}
	for _, tc := range tests {
		if got := classifyContentType(tc.ct); got != tc.want {
			t.Errorf("classifyContentType(%q) = %q, want %q", tc.ct, got, tc.want)
		}
	}
}

// TestParseCharset 覆盖 parseCharset：提取 charset 参数（小写化、去引号），
// 无声明 / 非法时回落 "utf-8"。
func TestParseCharset(t *testing.T) {
	tests := []struct {
		ct   string
		want string
	}{
		{"text/html; charset=UTF-8", "utf-8"},
		{"text/html; charset=ISO-8859-1", "iso-8859-1"},
		{"text/plain; charset=\"UTF-8\"", "utf-8"},
		{"text/html", "utf-8"},
		{"", "utf-8"},
		{"text/html; charset=", "utf-8"},
	}
	for _, tc := range tests {
		if got := parseCharset(tc.ct); got != tc.want {
			t.Errorf("parseCharset(%q) = %q, want %q", tc.ct, got, tc.want)
		}
	}
}

// TestDecodeBody 覆盖 decodeBody：utf-8/us-ascii 按 UTF-8 容错读，非法字节保留
// （不失败）；其他声明编码按原始字节转 string（已知裁剪，零依赖）。
func TestDecodeBody(t *testing.T) {
	utf8 := []byte("你好 world")
	for _, cs := range []string{"utf-8", "UTF-8", "us-ascii", "ascii", ""} {
		if got := decodeBody(utf8, cs); got != "你好 world" {
			t.Errorf("decodeBody(_, %q) = %q, want 你好 world", cs, got)
		}
	}
	// 非法 UTF-8 字节不失败：按原始字节保留（容错读）。
	invalid := []byte{'h', 0xff, 'i'}
	if got := decodeBody(invalid, "utf-8"); got != string(invalid) {
		t.Errorf("decodeBody with invalid utf-8 = %q, want raw bytes %q", got, string(invalid))
	}
	// 其他声明编码不转码：按原始字节转 string。
	if got := decodeBody(invalid, "iso-8859-1"); got != string(invalid) {
		t.Errorf("decodeBody with iso-8859-1 = %q, want raw bytes %q", got, string(invalid))
	}
	if got := decodeBody(invalid, "utf-16"); got != string(invalid) {
		t.Errorf("decodeBody with utf-16 = %q, want raw bytes %q", got, string(invalid))
	}
}

// TestIsSameOrigin 覆盖 isSameOrigin：scheme+host+port 相同才同源；缺省端口
// 归一化；nil 安全。
func TestIsSameOrigin(t *testing.T) {
	must := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}
	tests := []struct {
		name string
		a, b *url.URL
		want bool
	}{
		{"same host+path", must("https://example.com/a"), must("https://example.com/b"), true},
		{"host case-insensitive", must("https://EXAMPLE.com/a"), must("https://example.com/b"), true},
		{"default port normalized", must("https://example.com/a"), must("https://example.com:443/b"), true},
		{"http default port normalized", must("http://example.com/a"), must("http://example.com:80/b"), true},
		{"explicit same port", must("http://example.com:8080/a"), must("http://example.com:8080/b"), true},
		{"different scheme", must("https://example.com/a"), must("http://example.com/a"), false},
		{"different host", must("https://example.com/a"), must("https://other.example/a"), false},
		{"different port", must("https://example.com:8443/a"), must("https://example.com/a"), false},
		{"explicit different ports", must("http://a.com:8080/"), must("http://a.com:9090/"), false},
	}
	for _, tc := range tests {
		if got := isSameOrigin(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: isSameOrigin(%v, %v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
	// nil 安全。
	if isSameOrigin(nil, must("https://example.com")) {
		t.Error("isSameOrigin(nil, url) = true, want false")
	}
	if isSameOrigin(must("https://example.com"), nil) {
		t.Error("isSameOrigin(url, nil) = true, want false")
	}
	if isSameOrigin(nil, nil) {
		t.Error("isSameOrigin(nil, nil) = true, want false")
	}
}

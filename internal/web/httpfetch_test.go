package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHttpFetchDefaults 覆盖 NewHttpFetchProvider 的 0→默认规则。
func TestHttpFetchDefaults(t *testing.T) {
	p := NewHttpFetchProvider(FetchLimits{})
	if p.ID() != httpFetchProviderID {
		t.Fatalf("ID = %q, want %q", p.ID(), httpFetchProviderID)
	}
	if !p.Available() {
		t.Fatal("Available() = false, want true")
	}
	if p.limits.MaxURLBytes != defaultMaxURLBytes || p.limits.MaxResponseBytes != defaultMaxResponseBytes ||
		p.limits.MaxBodyChars != defaultMaxBodyChars || p.limits.TimeoutMs != defaultFetchTimeoutMs ||
		p.limits.MaxRedirects != defaultMaxRedirects || p.limits.UserAgent != defaultFetchUserAgent {
		t.Fatalf("defaults not applied: %+v", p.limits)
	}
}

// TestHttpFetchSuccessHTML 覆盖成功 html 抓取：分类正确 + body 解码 + User-Agent。
func TestHttpFetchSuccessHTML(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("user-agent")
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<h1>你好</h1>"))
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{UserAgent: "test-agent/1.0"})
	res, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL + "/page"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotUA != "test-agent/1.0" {
		t.Errorf("user-agent = %q, want test-agent/1.0", gotUA)
	}
	if res.URL != srv.URL+"/page" {
		t.Errorf("URL = %q, want %q", res.URL, srv.URL+"/page")
	}
	if res.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", res.StatusCode)
	}
	if res.Body.Kind != "html" {
		t.Errorf("Kind = %q, want html", res.Body.Kind)
	}
	if res.Body.Content != "<h1>你好</h1>" {
		t.Errorf("Content = %q, want <h1>你好</h1>", res.Body.Content)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false")
	}
}

// TestHttpFetchTextContentType 覆盖 text 类 content-type 分类（application/json）。
func TestHttpFetchTextContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{})
	res, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Body.Kind != "text" {
		t.Errorf("Kind = %q, want text", res.Body.Kind)
	}
	if res.Body.Content != `{"ok":true}` {
		t.Errorf("Content = %q", res.Body.Content)
	}
}

// TestHttpFetchUnsupportedContentType 覆盖 unsupported（二进制）→ ErrProvider
// （WebFetchBody 是 html/text 封闭联合，不表示二进制）。
func TestHttpFetchUnsupportedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{})
	_, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider", err)
	}
}

// TestHttpFetchNon2xxIsResult 覆盖非 2xx 是结果：StatusCode 保留、body 尽力返回、
// 不报错。
func TestHttpFetchNon2xxIsResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte("<p>missing</p>"))
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{})
	res, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", res.StatusCode)
	}
	if res.Body.Content != "<p>missing</p>" {
		t.Errorf("Content = %q, want <p>missing</p>", res.Body.Content)
	}
}

// TestHttpFetchSameOriginRedirect 覆盖同源重定向跟随（相对 Location，≤ 上限）：
// 最终 URL 与内容来自最后一跳。
func TestHttpFetchSameOriginRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			http.Redirect(w, r, "/c", http.StatusMovedPermanently)
		default:
			w.Header().Set("content-type", "text/html")
			_, _ = w.Write([]byte("final-content"))
		}
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{MaxRedirects: 5})
	res, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL + "/a"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.URL != srv.URL+"/c" {
		t.Errorf("URL = %q, want %q (final after redirects)", res.URL, srv.URL+"/c")
	}
	if res.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", res.StatusCode)
	}
	if res.Body.Content != "final-content" {
		t.Errorf("Content = %q, want final-content", res.Body.Content)
	}
}

// TestHttpFetchCrossOriginRedirect 覆盖跨源重定向 → ErrRedirectBlocked。
func TestHttpFetchCrossOriginRedirect(t *testing.T) {
	var otherHit bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHit = true
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte("other"))
	}))
	defer other.Close()
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer main.Close()

	p := NewHttpFetchProvider(FetchLimits{})
	_, err := p.Fetch(context.Background(), WebFetchRequest{URL: main.URL})
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("err = %v, want ErrRedirectBlocked", err)
	}
	if otherHit {
		t.Fatal("cross-origin redirect target was contacted")
	}
	if !strings.Contains(err.Error(), "fetch that URL directly") {
		t.Errorf("err = %q, want the cross-origin hint", err)
	}
}

// TestHttpFetchTooManyRedirects 覆盖超跳数 → ErrRedirectBlocked。
func TestHttpFetchTooManyRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound) // 自己重定向自己
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{MaxRedirects: 2})
	_, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL + "/loop"})
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("err = %v, want ErrRedirectBlocked", err)
	}
}

// TestHttpFetchMaxResponseBytesTruncated 覆盖超 MaxResponseBytes → 截断置位、
// body 为前 MaxResponseBytes 字节。
func TestHttpFetchMaxResponseBytesTruncated(t *testing.T) {
	body := strings.Repeat("x", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{MaxResponseBytes: 16, MaxBodyChars: 100000})
	res, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if res.Body.Content != strings.Repeat("x", 16) {
		t.Errorf("Content = %q, want the first 16 bytes", res.Body.Content)
	}
}

// TestHttpFetchMaxBodyCharsTruncated 覆盖超 MaxBodyChars → 字符截断置位
// （Unicode-safe：不劈开多字节字符）。
func TestHttpFetchMaxBodyCharsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("你好世界你好世界")) // 12 runes
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{MaxBodyChars: 4, MaxResponseBytes: 1 << 20})
	res, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if res.Body.Content != "你好世界" {
		t.Errorf("Content = %q, want the first 4 runes 你好世界", res.Body.Content)
	}
}

// TestHttpFetchCancelled 覆盖 ctx 取消 → ErrAborted。
func TestHttpFetchCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("hi"))
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Fetch(ctx, WebFetchRequest{URL: srv.URL}); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
}

// TestHttpFetchTimeout 覆盖抓取超时 → ErrTimeout。
func TestHttpFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()

	p := NewHttpFetchProvider(FetchLimits{TimeoutMs: 50})
	if _, err := p.Fetch(context.Background(), WebFetchRequest{URL: srv.URL}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

// TestHttpFetchInvalidURL 覆盖非法 URL → ErrInvalidURL。
func TestHttpFetchInvalidURL(t *testing.T) {
	p := NewHttpFetchProvider(FetchLimits{MaxURLBytes: 2048})
	cases := []struct {
		name string
		url  string
	}{
		{"non-http scheme", "ftp://example.com/file"},
		{"missing host", "https:///path"},
		{"userinfo", "https://user:pass@example.com/"},
		{"too long", "https://example.com/" + strings.Repeat("a", 2100)},
	}
	for _, tc := range cases {
		if _, err := p.Fetch(context.Background(), WebFetchRequest{URL: tc.url}); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("%s: err = %v, want ErrInvalidURL", tc.name, err)
		}
	}
	// 恰好等于上限的 URL 合法（inclusive 上界）；超一字节即拒绝。
	ok := NewHttpFetchProvider(FetchLimits{MaxURLBytes: 40})
	goodURL := "https://example.com/" + strings.Repeat("a", 40-len("https://example.com/"))
	if _, err := ok.validateFetchURL(goodURL); err != nil {
		t.Errorf("at-limit URL should be accepted, got %v", err)
	}
	over := "https://example.com/" + strings.Repeat("a", 40-len("https://example.com/")+1)
	if _, err := ok.validateFetchURL(over); !errors.Is(err, ErrInvalidURL) {
		t.Errorf("over-limit URL: err = %v, want ErrInvalidURL", err)
	}
}

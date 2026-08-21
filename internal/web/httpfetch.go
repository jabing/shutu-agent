// httpfetch.go — the local HTTP(S) fetch provider (dispatch-m7-2 §2, mirroring
// dsh web-fetch-http/src/provider.ts): validates URLs, follows only same-origin
// redirects, enforces hard time and size limits, classifies and decodes the
// body, and leaves presentation (HTML→Markdown) to the web_fetch tool. Requests
// carry no cookies or ambient credentials. SSRF/private-network protection is
// not implemented (ADR 后果: 个人单机默认可信，已知限制 — do not enable where the
// provider can reach sensitive internal targets).
package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// 默认硬上限（与 internal/config WebConfig 的默认值一致；NewHttpFetchProvider
// 对 <=0 的值落回这些默认，使直接构造也安全 — 镜像 DeepSeek provider 的 0→默认）。
const (
	defaultMaxURLBytes      = 2048
	defaultMaxResponseBytes = 2 << 20 // 2 MiB
	defaultMaxBodyChars     = 200000
	defaultFetchTimeoutMs   = 30000
	defaultMaxRedirects     = 5
	defaultFetchUserAgent   = "shutu-agent/0.1 (M7)"
)

// httpFetchProviderID 是抓取 provider 的稳定 id。
const httpFetchProviderID = "http"

// HttpFetchProvider 是抓取能力默认后端（id "http"）：只取公开 http(s) 资源，
// 无 cookie/凭据；跟随同源重定向；强制超时与大小上限；按 content-type 分类
// 并解码。SSRF/内网保护不实现（ADR 后果：个人单机默认可信，已知限制）。
type HttpFetchProvider struct {
	limits FetchLimits
}

// FetchLimits 是 HttpFetchProvider 的硬上限（来自 WebConfig，默认值见注释）。
type FetchLimits struct {
	MaxURLBytes      int    // 请求 URL 最大长度（默认 2048）
	MaxResponseBytes int    // 响应体最大字节（读超即截断；默认 2MiB）
	MaxBodyChars     int    // 解码后最大字符（截断；默认 200000）
	TimeoutMs        int    // 单次抓取超时毫秒（默认 30000）
	MaxRedirects     int    // 同源重定向最大跳数（默认 5）
	UserAgent        string // 默认 "shutu-agent/0.1 (M7)"
}

// NewHttpFetchProvider 返回 HttpFetchProvider（Available 恒 true，匿名公开抓取）。
// 对 <=0 的数值上限与空 UserAgent 落回默认值。
func NewHttpFetchProvider(limits FetchLimits) *HttpFetchProvider {
	p := &HttpFetchProvider{limits: limits}
	if p.limits.MaxURLBytes <= 0 {
		p.limits.MaxURLBytes = defaultMaxURLBytes
	}
	if p.limits.MaxResponseBytes <= 0 {
		p.limits.MaxResponseBytes = defaultMaxResponseBytes
	}
	if p.limits.MaxBodyChars <= 0 {
		p.limits.MaxBodyChars = defaultMaxBodyChars
	}
	if p.limits.TimeoutMs <= 0 {
		p.limits.TimeoutMs = defaultFetchTimeoutMs
	}
	if p.limits.MaxRedirects <= 0 {
		p.limits.MaxRedirects = defaultMaxRedirects
	}
	if p.limits.UserAgent == "" {
		p.limits.UserAgent = defaultFetchUserAgent
	}
	return p
}

// ID 返回稳定 id "http"。
func (p *HttpFetchProvider) ID() string { return httpFetchProviderID }

// Available 恒 true：匿名公开抓取无凭证可检查（对照 dsh LOCAL_FETCH_PROVIDER_ID
// 的 available() 恒 true）。
func (p *HttpFetchProvider) Available() bool { return true }

// Fetch 抓取 req.URL。行为规格（dispatch-m7-2 §2）：
//  1. validateFetchURL 校验；2. 单次 GET（自定义 CheckRedirect 不自动跟随，
//     重定向由本 provider 手动处理）；3. 同源重定向跟随（≤ MaxRedirects，目标
//     重新校验）；4. 响应体按 MaxResponseBytes 截断（不报错）；5. 非 2xx 是
//     结果（StatusCode 保留，body 尽力分类/解码）；6. 整个请求+读取包在
//     context.WithTimeout(ctx, TimeoutMs)。
func (p *HttpFetchProvider) Fetch(ctx context.Context, req WebFetchRequest) (WebFetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.limits.TimeoutMs)*time.Millisecond)
	defer cancel()

	u, err := p.validateFetchURL(req.URL)
	if err != nil {
		return WebFetchResult{}, err
	}
	for hop := 0; ; hop++ {
		resp, err := p.doGet(ctx, u)
		if err != nil {
			return WebFetchResult{}, err
		}
		if !isRedirectStatus(resp.StatusCode) {
			return p.readResult(ctx, resp, u.String())
		}
		// 重定向预算在解析/校验目标前先检查（镜像 dsh：先查跳数再处理）。
		if hop >= p.limits.MaxRedirects {
			resp.Body.Close()
			return WebFetchResult{}, fmt.Errorf("%w: exceeded the maximum of %d redirects", ErrRedirectBlocked, p.limits.MaxRedirects)
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			resp.Body.Close()
			return WebFetchResult{}, fmt.Errorf("%w: redirect (HTTP %d) without a Location header", ErrRedirectBlocked, resp.StatusCode)
		}
		next, perr := url.Parse(loc)
		if perr != nil {
			resp.Body.Close()
			return WebFetchResult{}, fmt.Errorf("%w: invalid redirect Location %q: %v", ErrProvider, loc, perr)
		}
		resolved := u.ResolveReference(next)
		if !isSameOrigin(u, resolved) {
			resp.Body.Close()
			return WebFetchResult{}, fmt.Errorf("%w: cross-origin redirect to %s is not followed automatically; fetch that URL directly", ErrRedirectBlocked, resolved)
		}
		// 目标 URL 重新校验（长度/scheme/host/userinfo）——重定向不能成为绕过
		// validateFetchURL 的后门（镜像 dsh）。
		validated, verr := p.validateFetchURL(resolved.String())
		if verr != nil {
			resp.Body.Close()
			return WebFetchResult{}, verr
		}
		resp.Body.Close()
		u = validated
	}
}

// validateFetchURL 校验抓取 URL（规格 §2.1）：仅 http:/https:；host 非空；
// 总长 ≤ MaxURLBytes；其余（userinfo、非法字符等）→ ErrInvalidURL。
func (p *HttpFetchProvider) validateFetchURL(raw string) (*url.URL, error) {
	if len(raw) > p.limits.MaxURLBytes {
		return nil, fmt.Errorf("%w: url exceeds the maximum length of %d", ErrInvalidURL, p.limits.MaxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid url: %v", ErrInvalidURL, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%w: unsupported scheme %q (only http and https are allowed)", ErrInvalidURL, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in urls are not allowed", ErrInvalidURL)
	}
	return u, nil
}

// doGet 发起单次 GET。CheckRedirect 返回 http.ErrUseLastResponse（不自动跟随、
// 把 3xx 响应原样返回），重定向由 Fetch 手动处理。请求携带 User-Agent 与
// Accept（照 dsh requestOnce）。错误映射：ctx 取消 → ErrAborted；超时 →
// ErrTimeout；其余传输/网络错误 → ErrProvider。
func (p *HttpFetchProvider) doGet(ctx context.Context, u *url.URL) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrProvider, err)
	}
	httpReq.Header.Set("user-agent", p.limits.UserAgent)
	httpReq.Header.Set("accept", "text/html,application/xhtml+xml,text/*;q=0.9,application/json;q=0.8")
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		if ctx.Err() != nil {
			return nil, ErrAborted
		}
		return nil, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	return resp, nil
}

// readResult 读取最终响应体（规格 §2.4/§2.5）：io.LimitReader 读到
// MaxResponseBytes+1，超限截断并置 Truncated（不报错）；非 2xx 也是结果
// （StatusCode 保留，body 尽力分类/解码）；content-type 为 unsupported
// （二进制）→ ErrProvider（WebFetchBody 是 html/text 封闭联合，新增 kind 是
// 协调变更——对照 dsh 的 WEB_UNSUPPORTED_CONTENT_TYPE）。
func (p *HttpFetchProvider) readResult(ctx context.Context, resp *http.Response, finalURL string) (WebFetchResult, error) {
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	kind := classifyContentType(ct)
	if kind == "unsupported" {
		return WebFetchResult{}, fmt.Errorf("%w: unsupported content type %q", ErrProvider, ct)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(p.limits.MaxResponseBytes)+1))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return WebFetchResult{}, ErrTimeout
		}
		if ctx.Err() != nil {
			return WebFetchResult{}, ErrAborted
		}
		return WebFetchResult{}, fmt.Errorf("%w: read body: %v", ErrProvider, err)
	}
	truncated := len(data) > p.limits.MaxResponseBytes
	if truncated {
		data = data[:p.limits.MaxResponseBytes]
	}
	body := decodeBody(data, parseCharset(ct))
	// 字符上限（Unicode-safe 截断，不劈开多字节字符）。
	if runes := utf8.RuneCountInString(body); runes > p.limits.MaxBodyChars {
		body = string([]rune(body)[:p.limits.MaxBodyChars])
		truncated = true
	}
	return WebFetchResult{
		URL:        finalURL,
		StatusCode: resp.StatusCode,
		Body:       WebFetchBody{Kind: kind, Content: body},
		Truncated:  truncated,
	}, nil
}

// isRedirectStatus 判断状态码是否为可携带 Location 的 3xx 重定向。
func isRedirectStatus(status int) bool {
	return status >= 300 && status < 400
}

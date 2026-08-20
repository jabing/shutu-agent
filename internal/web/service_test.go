package web

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeSearchProvider 是可编程的 SearchProvider 替身。
type fakeSearchProvider struct {
	id      string
	sources []WebSearchSource
	err     error
	gotCtx  context.Context
	gotReq  WebSearchRequest
}

func (f *fakeSearchProvider) ID() string { return f.id }
func (f *fakeSearchProvider) Available() bool {
	return true
}
func (f *fakeSearchProvider) Search(ctx context.Context, req WebSearchRequest) (WebSearchResult, error) {
	f.gotCtx = ctx
	f.gotReq = req
	if f.err != nil {
		return WebSearchResult{}, f.err
	}
	return WebSearchResult{Sources: f.sources}, nil
}

// fakeFetchProvider 是可编程的 FetchProvider 替身。
type fakeFetchProvider struct {
	id     string
	result WebFetchResult
	err    error
	gotCtx context.Context
	gotReq WebFetchRequest
}

func (f *fakeFetchProvider) ID() string { return f.id }
func (f *fakeFetchProvider) Available() bool {
	return true
}
func (f *fakeFetchProvider) Fetch(ctx context.Context, req WebFetchRequest) (WebFetchResult, error) {
	f.gotCtx = ctx
	f.gotReq = req
	if f.err != nil {
		return WebFetchResult{}, f.err
	}
	return f.result, nil
}

func nSources(n int) []WebSearchSource {
	out := make([]WebSearchSource, n)
	for i := range out {
		out[i] = WebSearchSource{URL: fmt.Sprintf("https://s%d.example", i)}
	}
	return out
}

// TestEngineRegisterDuplicateID 覆盖注册表：重复 id 报错；search 与 fetch
// 注册表相互独立（同 id 跨能力允许）。
func TestEngineRegisterDuplicateID(t *testing.T) {
	e := NewEngine()
	if err := e.RegisterSearchProvider(&fakeSearchProvider{id: "a"}); err != nil {
		t.Fatalf("first search register: %v", err)
	}
	if err := e.RegisterSearchProvider(&fakeSearchProvider{id: "a"}); err == nil {
		t.Fatal("duplicate search id: want error, got nil")
	}
	if err := e.RegisterFetchProvider(&fakeFetchProvider{id: "b"}); err != nil {
		t.Fatalf("first fetch register: %v", err)
	}
	if err := e.RegisterFetchProvider(&fakeFetchProvider{id: "b"}); err == nil {
		t.Fatal("duplicate fetch id: want error, got nil")
	}
	// 同 id 跨 search/fetch 注册表允许。
	if err := e.RegisterSearchProvider(&fakeSearchProvider{id: "b"}); err != nil {
		t.Fatalf("same id across capabilities should be allowed: %v", err)
	}
	if err := e.RegisterFetchProvider(&fakeFetchProvider{id: "a"}); err != nil {
		t.Fatalf("same id across capabilities should be allowed: %v", err)
	}
}

// TestEngineUnknownProvider 覆盖未注册 id：Search / Fetch 都返回 ErrNoProvider。
func TestEngineUnknownProvider(t *testing.T) {
	e := NewEngine()
	if _, err := e.Search(context.Background(), "missing", WebSearchRequest{Query: "x"}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("Search: err = %v, want ErrNoProvider", err)
	}
	if _, err := e.Fetch(context.Background(), "missing", WebFetchRequest{URL: "https://x.example"}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("Fetch: err = %v, want ErrNoProvider", err)
	}
}

// TestEngineSearchTruncates 覆盖截断：MaxResults>0 截断并置 Truncated；
// <=0 不截断不置位；等于上限不截断。
func TestEngineSearchTruncates(t *testing.T) {
	p := &fakeSearchProvider{id: "ds", sources: nSources(5)}
	e := NewEngine()
	if err := e.RegisterSearchProvider(p); err != nil {
		t.Fatalf("register: %v", err)
	}

	res, err := e.Search(context.Background(), "ds", WebSearchRequest{Query: "q", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Sources) != 3 {
		t.Fatalf("len(Sources) = %d, want 3", len(res.Sources))
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	// MaxResults 原样透传给 provider（截断发生在返回路径，而非请求路径）。
	if p.gotReq.MaxResults != 3 {
		t.Fatalf("provider got MaxResults = %d, want 3", p.gotReq.MaxResults)
	}

	for _, max := range []int{0, -1} {
		res, err = e.Search(context.Background(), "ds", WebSearchRequest{Query: "q", MaxResults: max})
		if err != nil {
			t.Fatalf("Search(max=%d): %v", max, err)
		}
		if len(res.Sources) != 5 {
			t.Fatalf("max=%d: len(Sources) = %d, want 5", max, len(res.Sources))
		}
		if res.Truncated {
			t.Fatalf("max=%d: Truncated = true, want false", max)
		}
	}

	// 恰好等于上限 → 不截断。
	res, err = e.Search(context.Background(), "ds", WebSearchRequest{Query: "q", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search(max=5): %v", err)
	}
	if len(res.Sources) != 5 || res.Truncated {
		t.Fatalf("max=5: len=%d Truncated=%v, want 5/false", len(res.Sources), res.Truncated)
	}
}

// TestEngineSearchPassesCancellation 覆盖 Engine.Search 透传 ctx 取消：
// provider 收到已取消的 ctx 并返回 ErrAborted，Engine 原样透传。
func TestEngineSearchPassesCancellation(t *testing.T) {
	p := &fakeSearchProvider{id: "ds", err: ErrAborted}
	e := NewEngine()
	if err := e.RegisterSearchProvider(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := e.Search(ctx, "ds", WebSearchRequest{Query: "q"}); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if p.gotCtx.Err() == nil {
		t.Fatal("provider did not receive the cancelled ctx")
	}
}

// TestEngineSearchPassesProviderError 覆盖 provider 错误原样透传（不重写、
// 不吞掉），截断逻辑不掩盖错误。
func TestEngineSearchPassesProviderError(t *testing.T) {
	sentinel := errors.New("boom")
	p := &fakeSearchProvider{id: "ds", err: sentinel}
	e := NewEngine()
	if err := e.RegisterSearchProvider(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := e.Search(context.Background(), "ds", WebSearchRequest{Query: "q", MaxResults: 1}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the provider's error", err)
	}
}

// TestEngineFetchPassthrough 覆盖 Fetch 不截断、结果原样透传。
func TestEngineFetchPassthrough(t *testing.T) {
	prov := &fakeFetchProvider{id: "http", result: WebFetchResult{
		URL: "https://final.example", StatusCode: 200,
		Body: WebFetchBody{Kind: "html", Content: "<p>hi</p>"},
	}}
	e := NewEngine()
	if err := e.RegisterFetchProvider(prov); err != nil {
		t.Fatalf("register: %v", err)
	}
	res, err := e.Fetch(context.Background(), "http", WebFetchRequest{URL: "https://orig.example"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if prov.gotReq.URL != "https://orig.example" {
		t.Fatalf("provider got URL %q", prov.gotReq.URL)
	}
	if res.URL != "https://final.example" || res.StatusCode != 200 || res.Body.Kind != "html" || res.Truncated {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestSearchRequestEventConstructor 覆盖事件构造函数的防御性拷贝。
func TestSearchRequestEventConstructor(t *testing.T) {
	body := map[string]any{"model": "m", "nested": map[string]any{"x": 1}}
	ev := NewSearchRequestEvent("https://e/messages", "2023-06-01", "m", "q", body)
	if ev.Endpoint != "https://e/messages" || ev.Query != "q" || ev.APIVersion != "2023-06-01" || ev.Model != "m" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	// 修改原 body 不影响事件快照。
	body["model"] = "mutated"
	if ev.Body["model"] != "m" {
		t.Fatalf("event Body aliases caller map: %#v", ev.Body)
	}
}

// html.go — the lightweight zero-dependency HTML→Markdown converter
// (dispatch-m7-2 §3, mirroring dsh tool-web's turndown but hand-written against
// the standard library). It is a simplification, not a full HTML spec: it
// covers common document structure well enough for a model to read, and does
// NOT do tables, nested-list indentation, attribute handling beyond a/img, CSS/
// inline styles, or encoding sniffing (ADR 后果: 质量上限记录于此，避免误以为完整
// 渲染). A hand-written "<...>" scanner walks the source; tag names are
// lower-cased; entities are decoded with html.UnescapeString.
package web

import (
	"html"
	"net/url"
	"strings"
)

// HTMLToMarkdown 把一段 HTML 转成简化 Markdown（规则见派发文档 §3），
// 连续空行压缩为至多一个空行，输出前后 trim。
func HTMLToMarkdown(htmlStr string) string {
	m := &mdConv{src: htmlStr}
	m.run()
	return strings.TrimSpace(collapseBlankLines(m.out.String()))
}

// mdConv 是转换器状态：源码游标 + 输出 + 开放元素栈（含并行栈：a 的待定 href、
// li 的列表类型）。
type mdConv struct {
	src   string
	pos   int
	out   strings.Builder
	stack []string // 开放元素名（小写）
	hrefs []string // 与 stack 并行：开放 <a> 的 href（空 = 无/相对）
	typ   []string // 与 stack 并行：开放 <li> 的列表类型（"ul"/"ol"/""）
	quote int      // 开放 blockquote 深度
}

// run 扫描整个源码：文本直接写入（blockquote 内做每行 > 续接），标签走
// handleTag。结束后隐式关闭所有未闭合元素。
func (m *mdConv) run() {
	for m.pos < len(m.src) {
		lt := strings.IndexByte(m.src[m.pos:], '<')
		if lt < 0 {
			m.writeText(m.src[m.pos:])
			m.pos = len(m.src)
			break
		}
		if lt > 0 {
			m.writeText(m.src[m.pos : m.pos+lt])
			m.pos += lt
		}
		m.handleTag()
	}
	for len(m.stack) > 0 {
		m.closeTop()
	}
}

// handleTag 处理 m.pos 处的一个 '<'（标签 / 注释 / 声明 / 字面 '<'）。
func (m *mdConv) handleTag() {
	if strings.HasPrefix(m.src[m.pos:], "<!--") {
		if end := strings.Index(m.src[m.pos+4:], "-->"); end >= 0 {
			m.pos += 4 + end + 3
		} else {
			m.pos = len(m.src)
		}
		return
	}
	if m.pos+1 < len(m.src) && (m.src[m.pos+1] == '!' || m.src[m.pos+1] == '?') {
		// <!DOCTYPE ...> / <?...?>：跳过声明。
		if end := strings.IndexByte(m.src[m.pos+2:], '>'); end >= 0 {
			m.pos += 2 + end + 1
		} else {
			m.pos = len(m.src)
		}
		return
	}

	i := m.pos + 1
	closing := false
	if i < len(m.src) && m.src[i] == '/' {
		closing = true
		i++
	}
	j := i
	for j < len(m.src) && !isTagNameBreak(m.src[j]) {
		j++
	}
	name := strings.ToLower(m.src[i:j])
	// 非字母开头的 '<'（如 "5 < 6" 的 "< "）：按字面 '<' 处理。
	if name == "" || !isAsciiLetter(m.src[i]) {
		m.out.WriteString("<")
		m.pos++
		return
	}

	// 找结束 '>'（尊重引号）。
	k := j
	var quote byte
	for k < len(m.src) {
		c := m.src[k]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
		} else if c == '"' || c == '\'' {
			quote = c
		} else if c == '>' {
			break
		}
		k++
	}
	if k >= len(m.src) {
		// 无 '>' 的残缺标签：按字面文本保留。
		m.out.WriteString(html.UnescapeString(m.src[m.pos:]))
		m.pos = len(m.src)
		return
	}
	tagSrc := m.src[m.pos : k+1]
	m.pos = k + 1

	if closing {
		m.handleClose(name)
		return
	}
	attrs := parseAttrs(tagSrc)
	m.handleOpen(name, attrs)
}

// handleOpen 处理开放标签。
func (m *mdConv) handleOpen(name string, attrs map[string]string) {
	switch name {
	case "script", "style":
		// 内容整体丢弃。
		m.skipRawText(name)
		return
	case "br":
		m.blockBreak()
		return
	case "img":
		alt := strings.TrimSpace(attrs["alt"])
		src := strings.TrimSpace(attrs["src"])
		if src != "" {
			m.out.WriteString("![" + alt + "](" + src + ")")
		} else if alt != "" {
			m.out.WriteString(alt)
		}
		return
	case "h1", "h2", "h3", "h4", "h5", "h6":
		m.blockBreak()
		m.out.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
		m.push(name, "", "")
		return
	case "p":
		m.blockBreak()
		m.push(name, "", "")
		return
	case "div", "section", "article", "header", "footer", "nav", "main", "aside":
		m.blockBreak()
		m.push(name, "", "")
		return
	case "ul", "ol":
		m.blockBreak()
		m.push(name, "", "")
		return
	case "li":
		m.blockBreak()
		if len(m.stack) > 0 && m.stack[len(m.stack)-1] == "ol" {
			m.out.WriteString("1. ") // 简化编号（派发 §3：所有 <ol><li> 记 1.）
			m.push(name, "", "ol")
		} else {
			m.out.WriteString("- ")
			m.push(name, "", "ul")
		}
		return
	case "blockquote":
		m.quote++
		m.blockBreak()
		m.push(name, "", "")
		return
	case "pre":
		m.blockBreak()
		m.out.WriteString("```\n")
		m.push(name, "", "")
		return
	case "a":
		href := strings.TrimSpace(attrs["href"])
		m.push(name, href, "")
		if isAbsoluteHref(href) {
			m.out.WriteString("[")
		}
		return
	case "strong", "b":
		m.out.WriteString("**")
		m.push(name, "", "")
		return
	case "em", "i":
		m.out.WriteString("*")
		m.push(name, "", "")
		return
	case "code":
		m.out.WriteString("`")
		m.push(name, "", "")
		return
	default:
		// 其余未知标签：剥离标签、保留文本。
		m.push(name, "", "")
		return
	}
}

// handleClose 处理闭合标签：在栈上找匹配元素（含其上的未闭合元素一并隐式闭合），
// 应用该元素的收尾转换。
func (m *mdConv) handleClose(name string) {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i] != name {
			continue
		}
		switch name {
		case "h1", "h2", "h3", "h4", "h5", "h6", "p":
			m.out.WriteString("\n\n")
		case "blockquote":
			m.quote--
			if m.quote < 0 {
				m.quote = 0
			}
			m.out.WriteString("\n")
		case "pre":
			m.out.WriteString("\n```\n")
		case "a":
			if isAbsoluteHref(m.hrefs[i]) {
				m.out.WriteString("](" + m.hrefs[i] + ")")
			}
		case "strong", "b":
			m.out.WriteString("**")
		case "em", "i":
			m.out.WriteString("*")
		case "code":
			m.out.WriteString("`")
		case "div", "section", "article", "header", "footer", "nav", "main", "aside", "ul", "ol", "tr":
			m.blockBreak()
		}
		// 弹出该元素及之上的所有未闭合元素。
		m.stack = m.stack[:i]
		m.hrefs = m.hrefs[:i]
		m.typ = m.typ[:i]
		return
	}
	// 无匹配开放元素的孤立闭合标签：忽略。
}

// closeTop 隐式闭合栈顶元素（未闭合处理收尾）。
func (m *mdConv) closeTop() {
	m.handleClose(m.stack[len(m.stack)-1])
}

// push 把开放元素压栈（三个并行栈同步）。
func (m *mdConv) push(name, href, typ string) {
	m.stack = append(m.stack, name)
	m.hrefs = append(m.hrefs, href)
	m.typ = append(m.typ, typ)
}

// writeText 写入一段文本：实体解码；blockquote 内把换行续接为 "> "。
func (m *mdConv) writeText(s string) {
	if m.quote > 0 {
		s = strings.ReplaceAll(s, "\n", "\n> ")
	}
	m.out.WriteString(html.UnescapeString(s))
}

// blockBreak 写一个块级换行分隔；blockquote 内续接 "> "。
func (m *mdConv) blockBreak() {
	m.out.WriteString("\n")
	if m.quote > 0 {
		m.out.WriteString("> ")
	}
}

// skipRawText 丢弃 script/style 内容直到匹配的 </name>（不解释其中的类标签文本），
// 并消耗整个闭合标签。
func (m *mdConv) skipRawText(name string) {
	prefix := "</" + name
	lower := strings.ToLower(m.src)
	for m.pos < len(m.src) {
		idx := strings.Index(lower[m.pos:], prefix)
		if idx < 0 {
			m.pos = len(m.src)
			return
		}
		after := m.pos + idx + len(prefix)
		if after >= len(m.src) || isTagNameBreak(m.src[after]) {
			if after < len(m.src) && m.src[after] == '>' {
				m.pos = after + 1
			} else if end := strings.IndexByte(m.src[after:], '>'); end >= 0 {
				m.pos = after + end + 1
			} else {
				m.pos = len(m.src)
			}
			return
		}
		m.pos = m.pos + idx + 1
	}
}

// isAbsoluteHref 判断 href 是否为可生成 markdown 链接的绝对 http(s) URL；
// 相对/空 href 只保留链接文本（派发 §3）。
func isAbsoluteHref(href string) bool {
	u, err := url.Parse(href)
	if err != nil {
		return false
	}
	return u.IsAbs() && (u.Scheme == "http" || u.Scheme == "https")
}

// isTagNameBreak 判断字符是否为标签名的终止符。
func isTagNameBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '/', '>', '=':
		return true
	}
	return false
}

func isAsciiLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// parseAttrs 从标签源码（含 < >）提取属性 map：引号内值支持、布尔属性值取空串、
// 属性名小写化、值保留原样。a href / img alt / img src 是唯一消费方。
func parseAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	i := 0
	// 跳过 '<'、'/'、标签名。
	for i < len(tag) && (tag[i] == '<' || tag[i] == '/' || tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n' || tag[i] == '\r' || tag[i] == '>') {
		i++
	}
	for i < len(tag) && !isTagNameBreak(tag[i]) {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n' || tag[i] == '\r') {
			i++
		}
		if i >= len(tag) || tag[i] == '>' || tag[i] == '/' {
			break
		}
		// 属性名。
		nameStart := i
		for i < len(tag) && tag[i] != '=' && tag[i] != '>' && !isAttrSpace(tag[i]) {
			i++
		}
		name := strings.ToLower(tag[nameStart:i])
		// 跳到 '='。
		j := i
		for j < len(tag) && (tag[j] == ' ' || tag[j] == '\t' || tag[j] == '\n' || tag[j] == '\r') {
			j++
		}
		if j >= len(tag) || tag[j] != '=' {
			attrs[name] = "" // 布尔属性
			i = j
			continue
		}
		j++
		for j < len(tag) && (tag[j] == ' ' || tag[j] == '\t' || tag[j] == '\n' || tag[j] == '\r') {
			j++
		}
		if j < len(tag) && (tag[j] == '"' || tag[j] == '\'') {
			q := tag[j]
			j++
			valStart := j
			for j < len(tag) && tag[j] != q {
				j++
			}
			attrs[name] = tag[valStart:j]
			i = j + 1 // 跳过闭合引号
		} else {
			valStart := j
			for j < len(tag) && tag[j] != '>' && !isAttrSpace(tag[j]) {
				j++
			}
			attrs[name] = tag[valStart:j]
			i = j
		}
	}
	return attrs
}

func isAttrSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// collapseBlankLines 把连续空行压缩为至多一个空行（3+ 换行 → 2），并 trim 首尾
// 空行（输出前后 trim，派发 §3）。
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		blank := strings.TrimSpace(l) == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, l)
		prevBlank = blank
	}
	// 去尾部空行。
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	// 去头部空行。
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	return strings.Join(out, "\n")
}

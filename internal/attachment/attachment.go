// Package attachment 提供图片附件存储（M8-3，ADR 2026-08-20-m8-message-model.md）。
// 图片文件持久在 <data_dir>/attachments/，会话日志只存 ImageRef 引用（dsh 7078918
// 范式：落库只存引用，请求时才转 data URL）。零新依赖。
//
// 依赖方向是单向的：attachment 依赖 internal/llm（ImageRef 类型），而 llm 不依赖
// attachment（provider 只拿 ImageRef.Path 自行读文件，保持 llm 纯接缝——见 M8-3b）。
// 宽高不解析记 0（M8 裁剪：解码不做，ImageRef.Width/Height 仅作元数据）。
package attachment

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jabing/shutu-agent/internal/llm"
)

// SupportedMediaTypes 是受支持的图片媒体类型（扩展名 → media type，dsh 同款）。
var SupportedMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// Fail-closed sentinel errors from SaveImage (dispatch-m8-3 §2: 校验 mediaType
// 受支持、data 非空且 ≤ maxBytes，超限返回错误 fail-closed)。
var (
	// ErrUnsupportedType 是 mediaType 不在 SupportedMediaTypes 时的错误。
	ErrUnsupportedType = errors.New("attachment: unsupported media type")
	// ErrEmptyData 是 data 为空（len 0）时的错误。
	ErrEmptyData = errors.New("attachment: empty image data")
	// ErrTooLarge 是 data 超过 maxBytes 时的错误。
	ErrTooLarge = errors.New("attachment: image exceeds max bytes")
	// ErrNotFound 是 id 对应的附件文件不存在时的错误（P5 web 回显/发送用）。
	ErrNotFound = errors.New("attachment: image not found")
)

// Store 持久化图片附件到一个目录。不是并发安全的：主循环严格串行（D5）。
type Store struct{ dir string }

// NewStore 创建/打开附件目录（<dir> 不存在则 mkdir -p）。dir 空 → 默认
// <data_dir>/attachments。
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		dir = filepath.Join("data", "attachments")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("attachment: create dir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// SaveImage 把图片字节写入附件存储：校验 mediaType 受支持、data 非空且 ≤ maxBytes，
// 生成随机 id（hex），写 <dir>/<id><ext>，返回 ImageRef（ID/MediaType/Bytes/Width/
// Height/Path；宽高不解析记 0，M8 裁剪）。超限返回错误（fail-closed）。附件字节只
// 落附件文件，绝不进入会话日志——日志只存返回的 ImageRef（dsh 7078918）。
func (s *Store) SaveImage(mediaType string, data []byte, maxBytes int) (llm.ImageRef, error) {
	ext := extensionForMediaType(mediaType)
	if ext == "" {
		return llm.ImageRef{}, ErrUnsupportedType
	}
	if len(data) == 0 {
		return llm.ImageRef{}, ErrEmptyData
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return llm.ImageRef{}, ErrTooLarge
	}
	id, err := randomID()
	if err != nil {
		return llm.ImageRef{}, err
	}
	path := filepath.Join(s.dir, id+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return llm.ImageRef{}, fmt.Errorf("attachment: write %s: %w", path, err)
	}
	return llm.ImageRef{
		ID:        id,
		MediaType: mediaType,
		Bytes:     int64(len(data)),
		Width:     0,
		Height:    0,
		Path:      path,
	}, nil
}

// Read 按 ImageRef.Path 读回原始字节。Path 缺失/不可读返回错误。M8-3b 的 provider
// 序列化用它把图片字节转 data URL（请求时才读文件，内存与日志都不常驻字节）。
func (s *Store) Read(ref llm.ImageRef) ([]byte, error) {
	data, err := os.ReadFile(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("attachment: read %s: %w", ref.Path, err)
	}
	return data, nil
}

// GetByID 按附件 id 找回 ImageRef（扫描 <dir>/<id>.<ext>；宽高不解析记 0）。
// P5 web 回显（GET .../attachments/{id}）与发送带图（id → ImageRef）用它把
// 前端持有的 id 解析回持久引用。id 不存在返回 ErrNotFound。
func (s *Store) GetByID(id string) (llm.ImageRef, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return llm.ImageRef{}, ErrNotFound
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return llm.ImageRef{}, fmt.Errorf("attachment: list %s: %w", s.dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		dot := strings.LastIndexByte(name, '.')
		if dot <= 0 || name[:dot] != id {
			continue
		}
		mediaType := SupportedMediaTypes[name[dot:]]
		if mediaType == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		return llm.ImageRef{
			ID:        id,
			MediaType: mediaType,
			Bytes:     info.Size(),
			Width:     0,
			Height:    0,
			Path:      filepath.Join(s.dir, name),
		}, nil
	}
	return llm.ImageRef{}, fmt.Errorf("%w: %q", ErrNotFound, id)
}

// MediaTypeForExtension 按（点前缀）扩展名返回受支持的 media type；不受支持返回空
// 串。/attach 用它校验文件扩展名（dispatch-m8-3 §4 步骤 3）。
func MediaTypeForExtension(ext string) string {
	return SupportedMediaTypes[strings.ToLower(ext)]
}

// extensionForMediaType 返回受支持 media type 对应的文件扩展名（SupportedMediaTypes
// 的逆映射）。写成确定性 switch：.jpg/.jpeg 都映射 image/jpeg，固定落 .jpg。
func extensionForMediaType(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ""
}

// randomID 返回一个随机 hex id（16 字节 → 32 个 hex 字符）。
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("attachment: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

package contracts

import (
	"encoding/json"
	"fmt"
)

// JSON 值处理统一用 map[string]any / string / float64 / bool / nil 的原生承载,
// 本文件只提供 zod 镜像需要的少量判定,避免路由层重复手写断言。

// DecodeJSONObject:解析 JSON 文本且要求顶层为对象(数组/标量拒绝)。
// 等价 api 路由「请求体必须是 JSON 对象」的既有行为。
func DecodeJSONObject(data []byte) (map[string]any, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("请求体必须是 JSON 对象")
	}
	return obj, nil
}

// ObjectValue 取子对象(可选字段不存在返回 ok=false)。
func ObjectValue(m map[string]any, key string) (map[string]any, bool) {
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	obj, ok := v.(map[string]any)
	return obj, ok
}

func StringValue(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func IsNull(m map[string]any, key string) bool {
	v, ok := m[key]
	return ok && v == nil
}

// EncodeJSONCompact:与 TS JSON.stringify 等价的紧凑序列化(无缩进;
// 关键:禁用 Go 的 HTML 转义,<、>、& 必须原样,否则 ETag/版本哈希漂移)。
func EncodeJSONCompact(v any) ([]byte, error) {
	var buf jsonEncodeBuffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encoder 每次追加换行,JSON.stringify 没有,去掉。
	return buf.trimLastNewline(), nil
}

type jsonEncodeBuffer []byte

func (b *jsonEncodeBuffer) Write(p []byte) (int, error) { *b = append(*b, p...); return len(p), nil }

func (b *jsonEncodeBuffer) trimLastNewline() []byte {
	if n := len(*b); n > 0 && (*b)[n-1] == '\n' {
		return (*b)[:n-1]
	}
	return *b
}

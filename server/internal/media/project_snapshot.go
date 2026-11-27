package media

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// 回写已知字段时保留滚动升级带来的扩展数据。比较基线始终由 Repository.Save 使用原始快照。
func preserveProjectSnapshotFields(stored, updated []byte) ([]byte, error) {
	var previous Project
	if err := json.Unmarshal(stored, &previous); err != nil {
		return nil, err
	}
	known, err := json.Marshal(previous)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(stored, known) {
		return updated, nil
	}
	decode := func(raw []byte) (any, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		err := decoder.Decode(&value)
		return value, err
	}
	source, err := decode(stored)
	if err != nil {
		return nil, err
	}
	before, err := decode(known)
	if err != nil {
		return nil, err
	}
	if !projectHasExtraFields(source, before) {
		return updated, nil
	}
	after, err := decode(updated)
	if err != nil {
		return nil, err
	}
	merged, err := mergeProjectSnapshot(source, before, after)
	if err != nil {
		return nil, err
	}
	return json.Marshal(merged)
}

func projectHasExtraFields(stored, known any) bool {
	switch source := stored.(type) {
	case map[string]any:
		fields, _ := known.(map[string]any)
		for key, value := range source {
			before, ok := fields[key]
			if !ok || projectHasExtraFields(value, before) {
				return true
			}
		}
	case []any:
		items, _ := known.([]any)
		for i, item := range source {
			if i >= len(items) || projectHasExtraFields(item, items[i]) {
				return true
			}
		}
	}
	return false
}

func mergeProjectSnapshot(stored, known, updated any) (any, error) {
	if reflect.DeepEqual(known, updated) {
		return stored, nil
	}
	source, sourceObject := stored.(map[string]any)
	before, beforeObject := known.(map[string]any)
	after, afterObject := updated.(map[string]any)
	if sourceObject && beforeObject && afterObject {
		for key, value := range source {
			previous, understood := before[key]
			next, retained := after[key]
			if !understood && !retained {
				after[key] = value
			} else if understood && !retained && projectHasExtraFields(value, previous) {
				// 清空可选对象也会整体删除其中的新版字段，保留原快照供新版实例处理。
				return nil, ErrConflict
			} else if understood && retained {
				merged, err := mergeProjectSnapshot(value, previous, next)
				if err != nil {
					return nil, err
				}
				after[key] = merged
			}
		}
		return after, nil
	}
	// 数组中的镜头可能重排，按位置拷贝未知执行事实会将其挂到错误的镜头。
	// 遇到需要替换的新版数组时交给识别该格式的实例继续处理。
	if projectHasExtraFields(stored, known) {
		return nil, ErrConflict
	}
	return updated, nil
}

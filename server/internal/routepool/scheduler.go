// Package routepool 在同优先级供应商间按权重轮转，返回一次请求的无重复候选序列。
package routepool

import (
	"container/list"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const maxPools = 1024

type Candidate struct {
	ID       string
	Priority int64
	Weight   int64
}

type poolState struct {
	key       string
	signature string
	current   []int64
}

// Scheduler 的状态仅属于当前服务实例；并发请求共享一条轮转序列。
// LRU 限制旧模型/协议配置的状态数量，避免管理操作造成常驻内存持续增长。
type Scheduler struct {
	mu    sync.Mutex
	pools map[string]*list.Element
	lru   list.List
}

func New() *Scheduler { return &Scheduler{} }

// Order 返回 candidates 的原始下标。priority 小者先尝试；同一优先级的
// 首选使用平滑加权轮转，其余供应商只作本次请求的回退，不额外消耗轮转次数。
func (s *Scheduler) Order(key string, candidates []Candidate) []int {
	if len(candidates) == 0 {
		return nil
	}
	indices := make([]int, len(candidates))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := candidates[indices[i]], candidates[indices[j]]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.ID < b.ID
	})
	var signature strings.Builder
	for _, index := range indices {
		candidate := candidates[index]
		signature.WriteString(strconv.Itoa(len(candidate.ID)))
		signature.WriteByte(':')
		signature.WriteString(candidate.ID)
		signature.WriteByte(':')
		signature.WriteString(strconv.FormatInt(candidate.Priority, 10))
		signature.WriteByte(':')
		signature.WriteString(strconv.FormatInt(weight(candidate.Weight), 10))
		signature.WriteByte(';')
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pools == nil {
		s.pools = make(map[string]*list.Element)
	}
	element := s.pools[key]
	if element == nil {
		element = s.lru.PushFront(&poolState{key: key})
		s.pools[key] = element
		if s.lru.Len() > maxPools {
			oldest := s.lru.Back()
			delete(s.pools, oldest.Value.(*poolState).key)
			s.lru.Remove(oldest)
		}
	} else {
		s.lru.MoveToFront(element)
	}
	state := element.Value.(*poolState)
	// 供应商、优先级或权重变化后丢弃旧债务；输入顺序变化不重置轮转。
	if state.signature != signature.String() {
		state.signature = signature.String()
		state.current = make([]int64, len(candidates))
	}
	out := make([]int, 0, len(indices))
	for start := 0; start < len(indices); {
		end := start + 1
		for end < len(indices) && candidates[indices[end]].Priority == candidates[indices[start]].Priority {
			end++
		}
		selected, total := start, int64(0)
		for i := start; i < end; i++ {
			w := weight(candidates[indices[i]].Weight)
			total += w
			state.current[i] += w
			if state.current[i] > state.current[selected] {
				selected = i
			}
		}
		state.current[selected] -= total
		out = append(out, indices[selected])
		remaining := make([]int, 0, end-start-1)
		for i := start; i < end; i++ {
			if i != selected {
				remaining = append(remaining, i)
			}
		}
		sort.SliceStable(remaining, func(i, j int) bool {
			return state.current[remaining[i]] > state.current[remaining[j]]
		})
		for _, i := range remaining {
			out = append(out, indices[i])
		}
		start = end
	}
	return out
}

func weight(value int64) int64 {
	if value < 1 {
		return 1
	}
	if value > 10_000 {
		return 10_000
	}
	return value
}

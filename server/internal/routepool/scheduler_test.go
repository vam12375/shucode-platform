package routepool

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestEqualWeightsRotateAndKeepPriorityFallback(t *testing.T) {
	s := New()
	candidates := []Candidate{{ID: "backup", Priority: 200, Weight: 10}, {ID: "b", Priority: 100, Weight: 1}, {ID: "a", Priority: 100, Weight: 1}}
	for i := range 8 {
		order := s.Order("model", candidates)
		want := []int{2, 1, 0}
		if i%2 == 1 {
			want = []int{1, 2, 0}
		}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("request %d: got %v, want %v", i, order, want)
		}
	}
}

func TestSmoothWeightsAndIndependentPools(t *testing.T) {
	var s Scheduler
	candidates := []Candidate{{ID: "a", Weight: 5}, {ID: "b", Weight: 1}, {ID: "c", Weight: 1}}
	counts := make([]int, len(candidates))
	for range 700 {
		order := s.Order("model", candidates)
		counts[order[0]]++
		seen := map[int]bool{}
		for _, i := range order {
			if seen[i] {
				t.Fatal("one request cannot retry the same candidate twice")
			}
			seen[i] = true
		}
		if len(seen) != len(candidates) {
			t.Fatal("missing fallback")
		}
	}
	if !reflect.DeepEqual(counts, []int{500, 100, 100}) {
		t.Fatalf("incorrect distribution: %v", counts)
	}
	for _, key := range []string{"other-model", "different-protocol"} {
		if s.Order(key, candidates)[0] != 0 {
			t.Fatal("new pool must start independently")
		}
	}
}

func TestConfigurationChangesResetAndInputOrderDoesNot(t *testing.T) {
	s := New()
	candidates := []Candidate{{ID: "a", Weight: 1}, {ID: "b", Weight: 1}}
	if s.Order("model", candidates)[0] != 0 {
		t.Fatal("expected a")
	}
	if s.Order("model", []Candidate{candidates[1], candidates[0]})[0] != 0 {
		t.Fatal("reordered input must still choose b")
	}
	candidates[1].Weight = 3
	if s.Order("model", candidates)[0] != 1 {
		t.Fatal("changed weights must reset stale scores")
	}
	if !reflect.DeepEqual(s.Order("model", candidates[:1]), []int{0}) {
		t.Fatal("removed candidate must not remain a fallback")
	}
	if got := s.Order("model", nil); got != nil {
		t.Fatalf("empty candidates: %v", got)
	}
}

func TestConcurrentRotationIsExact(t *testing.T) {
	s := New()
	candidates := []Candidate{{ID: "a", Weight: 3}, {ID: "b", Weight: 1}}
	var mu sync.Mutex
	counts := []int{0, 0}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				selected := s.Order("model", candidates)[0]
				mu.Lock()
				counts[selected]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(counts, []int{300, 100}) {
		t.Fatalf("concurrent distribution: %v", counts)
	}
}

func TestPoolCacheIsBoundedAndRecentlyUsedSurvives(t *testing.T) {
	s := New()
	candidates := []Candidate{{ID: "a", Weight: 1}, {ID: "b", Weight: 1}}
	for i := range maxPools {
		s.Order(fmt.Sprint(i), candidates)
	}
	s.Order("0", candidates)
	s.Order("overflow", candidates)
	if len(s.pools) != maxPools || s.pools["0"] == nil || s.pools["1"] != nil {
		t.Fatal("LRU did not retain recent pool within bounded memory")
	}
}

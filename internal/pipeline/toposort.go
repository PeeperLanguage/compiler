package pipeline

import "compiler/internal/project"

type visitState int

const (
	visitNone visitState = iota
	visitTemp
	visitDone
)

func topoSort(mods []*project.Module, deps func(string) []string) ([]*project.Module, [][]string) {
	if len(mods) == 0 {
		return nil, nil
	}
	index := make(map[string]*project.Module, len(mods))
	for _, mod := range mods {
		if mod == nil || mod.Key == "" {
			continue
		}
		index[mod.Key] = mod
	}
	state := make(map[string]visitState, len(index))
	order := make([]*project.Module, 0, len(index))
	stack := make([]string, 0, len(index))
	cycles := make([][]string, 0)

	var visit func(string)
	visit = func(key string) {
		if key == "" {
			return
		}
		switch state[key] {
		case visitTemp:
			cycles = append(cycles, extractCycle(stack, key))
			return
		case visitDone:
			return
		}
		state[key] = visitTemp
		stack = append(stack, key)
		for _, dep := range deps(key) {
			if dep == "" {
				continue
			}
			if _, ok := index[dep]; ok {
				visit(dep)
			}
		}
		stack = stack[:len(stack)-1]
		state[key] = visitDone
		if mod := index[key]; mod != nil {
			order = append(order, mod)
		}
	}

	for key := range index {
		visit(key)
	}

	return order, cycles
}

func extractCycle(stack []string, key string) []string {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == key {
			cycle := append([]string{}, stack[i:]...)
			cycle = append(cycle, key)
			return cycle
		}
	}
	return []string{key}
}

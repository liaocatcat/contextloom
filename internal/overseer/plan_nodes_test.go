package overseer

import (
	"reflect"
	"testing"
)

func TestPlanNodes_EmptyTasks(t *testing.T) {
	result := PlanNodes(nil, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestPlanNodes_EmptySlice(t *testing.T) {
	result := PlanNodes([]string{}, map[string]int{})
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestPlanNodes_SingleTask(t *testing.T) {
	tasks := []string{"task1"}
	complexity := map[string]int{"task1": 1}
	result := PlanNodes(tasks, complexity)
	expected := []string{"task1"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllSimpleThreeTasks(t *testing.T) {
	tasks := []string{"task1", "task2", "task3"}
	complexity := map[string]int{"task1": 1, "task2": 1, "task3": 1}
	result := PlanNodes(tasks, complexity)
	expected := []string{"task1+task2+task3"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllSimpleFiveTasks(t *testing.T) {
	tasks := []string{"a", "b", "c", "d", "e"}
	complexity := map[string]int{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1}
	result := PlanNodes(tasks, complexity)
	expected := []string{"a+b+c", "d+e"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllSimpleSevenTasks(t *testing.T) {
	tasks := []string{"a", "b", "c", "d", "e", "f", "g"}
	complexity := map[string]int{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1, "f": 1, "g": 1}
	result := PlanNodes(tasks, complexity)
	expected := []string{"a+b+c", "d+e+f", "g"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllMediumTwoTasks(t *testing.T) {
	tasks := []string{"task1", "task2"}
	complexity := map[string]int{"task1": 2, "task2": 2}
	result := PlanNodes(tasks, complexity)
	expected := []string{"task1+task2"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllMediumThreeTasks(t *testing.T) {
	tasks := []string{"a", "b", "c"}
	complexity := map[string]int{"a": 2, "b": 2, "c": 2}
	result := PlanNodes(tasks, complexity)
	expected := []string{"a+b", "c"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllMediumFourTasks(t *testing.T) {
	tasks := []string{"a", "b", "c", "d"}
	complexity := map[string]int{"a": 2, "b": 2, "c": 2, "d": 2}
	result := PlanNodes(tasks, complexity)
	expected := []string{"a+b", "c+d"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllComplexTasksIndividual(t *testing.T) {
	tasks := []string{"c1", "c2", "c3"}
	complexity := map[string]int{"c1": 3, "c2": 4, "c3": 5}
	result := PlanNodes(tasks, complexity)
	expected := []string{"c1", "c2", "c3"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_ComplexityExactly3IsComplex(t *testing.T) {
	tasks := []string{"a", "b"}
	complexity := map[string]int{"a": 3, "b": 3}
	result := PlanNodes(tasks, complexity)
	expected := []string{"a", "b"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_MixedComplexitiesSameOrder(t *testing.T) {
	tasks := []string{"s1", "s2", "m1", "m2", "c1"}
	complexity := map[string]int{
		"s1": 1, "s2": 1,
		"m1": 2, "m2": 2,
		"c1": 3,
	}
	result := PlanNodes(tasks, complexity)
	expected := []string{"s1+s2", "m1+m2", "c1"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_MixedComplexitiesInterleaved(t *testing.T) {
	tasks := []string{"s1", "c1", "s2"}
	complexity := map[string]int{
		"s1": 1,
		"c1": 3,
		"s2": 1,
	}
	result := PlanNodes(tasks, complexity)
	expected := []string{"s1", "c1", "s2"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_TaskMissingComplexityDefaultsToOne(t *testing.T) {
	tasks := []string{"a", "b", "c"}
	// Only "a" has complexity defined; "b" and "c" default to 1
	complexity := map[string]int{"a": 1}
	result := PlanNodes(tasks, complexity)
	expected := []string{"a+b+c"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_AllTasksMissingComplexity(t *testing.T) {
	tasks := []string{"x", "y", "z", "w"}
	complexity := map[string]int{} // empty map, all default to 1
	result := PlanNodes(tasks, complexity)
	expected := []string{"x+y+z", "w"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_MixedWithBoundaryBatchSizes(t *testing.T) {
	// Test that batch boundaries are respected when complexities change
	tasks := []string{"s1", "s2", "m1", "s3"}
	complexity := map[string]int{
		"s1": 1, "s2": 1,
		"m1": 2,
		"s3": 1,
	}
	result := PlanNodes(tasks, complexity)
	expected := []string{"s1+s2", "m1", "s3"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_SimpleThenMediumBatchBoundary(t *testing.T) {
	// s1,s2,s3,s4 are simple (4 tasks, should be 3+1)
	// Then m1 is medium, forming its own node
	tasks := []string{"s1", "s2", "s3", "s4", "m1"}
	complexity := map[string]int{
		"s1": 1, "s2": 1, "s3": 1, "s4": 1,
		"m1": 2,
	}
	result := PlanNodes(tasks, complexity)
	expected := []string{"s1+s2+s3", "s4", "m1"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestPlanNodes_PreservesOriginalOrder(t *testing.T) {
	// Ensure nodes appear in the same order as the input tasks
	tasks := []string{"z", "a", "m"}
	complexity := map[string]int{"z": 3, "a": 1, "m": 2}
	result := PlanNodes(tasks, complexity)
	if len(result) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %v", len(result), result)
	}
	if result[0] != "z" {
		t.Errorf("expected first node 'z', got %q", result[0])
	}
	if result[1] != "a" {
		t.Errorf("expected second node 'a', got %q", result[1])
	}
	if result[2] != "m" {
		t.Errorf("expected third node 'm', got %q", result[2])
	}
}

func TestPlanNodes_LargeMixedBatch(t *testing.T) {
	// Comprehensive test with many tasks of varying complexities
	tasks := []string{
		"s1", "s2", "s3", "s4", "s5", // 5 simple → 3+2
		"m1", "m2", "m3", // 3 medium → 2+1
		"c1",       // 1 complex → 1
		"s6", "s7", // 2 simple → 2 (wait, same complexity, should be 2)
	}
	complexity := map[string]int{
		"s1": 1, "s2": 1, "s3": 1, "s4": 1, "s5": 1,
		"m1": 2, "m2": 2, "m3": 2,
		"c1": 5,
		"s6": 1, "s7": 1,
	}
	result := PlanNodes(tasks, complexity)
	expected := []string{
		"s1+s2+s3", "s4+s5",
		"m1+m2", "m3",
		"c1",
		"s6+s7",
	}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

package preprocessor

import (
	"sync"
	"testing"
)

// =============================================================================
// VocabManager.Map tests
// =============================================================================

func TestVocabManager_Map_KnownEntries(t *testing.T) {
	vm := NewVocabManager("")
	result := vm.Map([]string{"null_pointer", "authentication", "refactor"})
	expected := []string{"NPE", "AUTH", "REFACTOR"}
	assertEqualSlices(t, result, expected)
}

func TestVocabManager_Map_UnknownEntries(t *testing.T) {
	vm := NewVocabManager("")
	result := vm.Map([]string{"unknown_thing", "something_else"})
	// Unknown entries should be returned as-is
	expected := []string{"unknown_thing", "something_else"}
	assertEqualSlices(t, result, expected)
}

func TestVocabManager_Map_MixedEntries(t *testing.T) {
	vm := NewVocabManager("")
	result := vm.Map([]string{"null_pointer", "custom_field", "authentication"})
	expected := []string{"NPE", "custom_field", "AUTH"}
	assertEqualSlices(t, result, expected)
}

func TestVocabManager_Map_EmptySlice(t *testing.T) {
	vm := NewVocabManager("")
	result := vm.Map([]string{})
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestVocabManager_Map_PreservesOrder(t *testing.T) {
	vm := NewVocabManager("")
	input := []string{"sql_injection", "null_pointer", "type_hints", "bug_fix"}
	result := vm.Map(input)
	expected := []string{"SQLI", "NPE", "TYPING", "BUG_FIX"}
	assertEqualSlices(t, result, expected)
}

// =============================================================================
// VocabManager.Set tests
// =============================================================================

func TestVocabManager_Set_NewMapping(t *testing.T) {
	vm := NewVocabManager("")
	vm.Set("my_custom_key", "MCK")

	result := vm.Map([]string{"my_custom_key"})
	expected := []string{"MCK"}
	assertEqualSlices(t, result, expected)
}

func TestVocabManager_Set_OverrideExisting(t *testing.T) {
	vm := NewVocabManager("")

	// Default: "null_pointer" -> "NPE"
	result := vm.Map([]string{"null_pointer"})
	expected := []string{"NPE"}
	assertEqualSlices(t, result, expected)

	// Override
	vm.Set("null_pointer", "NPTR")
	result = vm.Map([]string{"null_pointer"})
	expected = []string{"NPTR"}
	assertEqualSlices(t, result, expected)
}

func TestVocabManager_Set_MultipleEntries(t *testing.T) {
	vm := NewVocabManager("")
	vm.Set("key_a", "VAL_A")
	vm.Set("key_b", "VAL_B")
	vm.Set("key_c", "VAL_C")

	result := vm.Map([]string{"key_a", "key_b", "key_c"})
	expected := []string{"VAL_A", "VAL_B", "VAL_C"}
	assertEqualSlices(t, result, expected)
}

// =============================================================================
// VocabManager.Load tests
// =============================================================================

func TestVocabManager_Load(t *testing.T) {
	vm := NewVocabManager("")
	err := vm.Load()
	if err != nil {
		t.Errorf("Load() should not error: %v", err)
	}

	// After Load, defaults should still be present (currently a stub that re-adds defaults)
	result := vm.Map([]string{"null_pointer", "authentication"})
	expected := []string{"NPE", "AUTH"}
	assertEqualSlices(t, result, expected)
}

// =============================================================================
// VocabManager Concurrency tests
// =============================================================================

func TestVocabManager_ConcurrentAccess(t *testing.T) {
	vm := NewVocabManager("")

	var wg sync.WaitGroup
	numGoroutines := 50
	numOps := 100

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				result := vm.Map([]string{"null_pointer", "authentication", "refactor"})
				if len(result) != 3 {
					t.Errorf("Map returned wrong length: %d", len(result))
					return
				}
			}
		}()
	}

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				vm.Set("concurrent_key", "CK")
			}
		}(i)
	}

	wg.Wait()

	// After concurrent access, the mapping should be consistent
	result := vm.Map([]string{"concurrent_key"})
	if len(result) != 1 || result[0] != "CK" {
		t.Errorf("expected ['CK'], got %v", result)
	}
}

func TestVocabManager_ConcurrentReadWrite(t *testing.T) {
	vm := NewVocabManager("")

	var wg sync.WaitGroup
	numOps := 200

	// Mixed reads and writes
	for i := 0; i < numOps; i++ {
		wg.Add(2)

		// Reader
		go func() {
			defer wg.Done()
			_ = vm.Map([]string{"null_pointer", "authentication", "refactor"})
		}()

		// Writer
		go func(idx int) {
			defer wg.Done()
			vm.Set("dynamic_key", "DK")
		}(i)
	}

	wg.Wait()

	// Verify the system is still consistent
	result := vm.Map([]string{"null_pointer", "authentication"})
	expected := []string{"NPE", "AUTH"}
	assertEqualSlices(t, result, expected)
}
